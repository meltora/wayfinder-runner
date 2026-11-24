package previewtunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"
)

const (
	IdleTimeout          = 5 * time.Minute
	CloseProtocolError   = 1002
	creditGrantThreshold = InitialWindow / 2
	idleSweepInterval    = 15 * time.Second
	maxCloseReason       = 123
)

type Conn interface {
	ReadMessage(ctx context.Context) ([]byte, error)
	WriteMessage(ctx context.Context, message []byte) error
	Close(code int, reason string) error
}

type Resolver interface {
	Resolve(ctx context.Context, request OpenRequest) (address string, ok bool)
}

type DialFunc func(ctx context.Context, address string) (net.Conn, error)

type Session struct {
	conn      Conn
	resolver  Resolver
	dial      DialFunc
	logger    *slog.Logger
	now       func() time.Time
	idleTicks <-chan time.Time

	mu         sync.Mutex
	outCond    *sync.Cond
	outbox     [][]byte
	ctx        context.Context
	done       bool
	writeErr   error
	streams    map[uint32]*stream
	live       map[uint32]*stream
	perPreview map[string]int
	highest    uint32
	unconsumed int64
	toClose    []net.Conn
	wg         sync.WaitGroup
}

type stream struct {
	id           uint32
	previewID    string
	ctx          context.Context
	cancel       context.CancelFunc
	cond         *sync.Cond
	upstream     net.Conn
	sendWindow   int64
	recvWindow   int64
	queue        [][]byte
	queued       int64
	ungranted    int64
	gatewayEnded bool
	runnerEnded  bool
	open         bool
	closed       bool
	lastData     time.Time
}

type closeWriter interface {
	CloseWrite() error
}

func NewSession(conn Conn, resolver Resolver, dial DialFunc, logger *slog.Logger, now func() time.Time) *Session {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	s := &Session{
		conn:       conn,
		resolver:   resolver,
		dial:       dial,
		logger:     logger,
		now:        now,
		streams:    map[uint32]*stream{},
		live:       map[uint32]*stream{},
		perPreview: map[string]int{},
	}
	s.outCond = sync.NewCond(&s.mu)
	return s
}

func (s *Session) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	s.wg.Add(2)
	go s.writeLoop(ctx, cancel)
	go s.idleLoop(ctx)
	err := s.readLoop(ctx)
	s.shutdown()
	if errors.Is(err, ErrProtocol) {
		_ = s.conn.Close(CloseProtocolError, closeReason(err))
	}
	cancel()
	s.wg.Wait()
	s.mu.Lock()
	writeErr := s.writeErr
	s.mu.Unlock()
	if writeErr != nil && !errors.Is(err, ErrProtocol) {
		return writeErr
	}
	return err
}

func (s *Session) ResetPreview(previewID string) {
	s.mu.Lock()
	ids := make([]uint32, 0)
	for id, st := range s.streams {
		if st.previewID == previewID {
			ids = append(ids, id)
		}
	}
	sortIDs(ids)
	for _, id := range ids {
		s.resetLocked(s.streams[id], ResetNotFound)
	}
	s.unlockAndClose()
}

func closeReason(err error) string {
	reason := err.Error()
	if len(reason) > maxCloseReason {
		reason = reason[:maxCloseReason]
	}
	return reason
}

func sortIDs(ids []uint32) {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
}

func (s *Session) unlockAndClose() {
	conns := s.toClose
	s.toClose = nil
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (s *Session) sendLocked(frame Frame) {
	if s.done {
		return
	}
	s.outbox = append(s.outbox, EncodeFrame(frame))
	s.outCond.Signal()
}

func resetFrame(id uint32, code ResetCode) Frame {
	return Frame{Type: FrameReset, StreamID: id, Payload: []byte{byte(code)}}
}

func windowFrame(id uint32, credit int64) Frame {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(credit))
	return Frame{Type: FrameWindow, StreamID: id, Payload: payload}
}

func (s *Session) writeLoop(ctx context.Context, cancel context.CancelFunc) {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		for len(s.outbox) == 0 && !s.done {
			s.outCond.Wait()
		}
		if s.done {
			s.outbox = nil
			s.mu.Unlock()
			return
		}
		message := s.outbox[0]
		s.outbox[0] = nil
		s.outbox = s.outbox[1:]
		s.mu.Unlock()
		if err := s.conn.WriteMessage(ctx, message); err != nil {
			s.mu.Lock()
			if s.writeErr == nil {
				s.writeErr = err
			}
			s.mu.Unlock()
			cancel()
			return
		}
	}
}

func (s *Session) idleLoop(ctx context.Context) {
	defer s.wg.Done()
	ticks := s.idleTicks
	if ticks == nil {
		ticker := time.NewTicker(idleSweepInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.sweepIdle()
		}
	}
}

func (s *Session) sweepIdle() {
	s.mu.Lock()
	now := s.now()
	ids := make([]uint32, 0)
	for id, st := range s.streams {
		if now.Sub(st.lastData) >= IdleTimeout {
			ids = append(ids, id)
		}
	}
	sortIDs(ids)
	for _, id := range ids {
		s.resetLocked(s.streams[id], ResetIdle)
	}
	s.unlockAndClose()
}

func (s *Session) readLoop(ctx context.Context) error {
	for {
		message, err := s.conn.ReadMessage(ctx)
		if err != nil {
			return err
		}
		frame, err := DecodeFrame(message)
		if err != nil {
			return err
		}
		if err := s.handle(frame); err != nil {
			return err
		}
	}
}

func (s *Session) handle(frame Frame) error {
	s.mu.Lock()
	defer s.unlockAndClose()
	if frame.Type == FrameOpen {
		return s.openLocked(frame)
	}
	st := s.streams[frame.StreamID]
	if st == nil {
		if frame.StreamID > s.highest {
			return protocolError("frame type %d for stream %d above the highest opened id %d", frame.Type, frame.StreamID, s.highest)
		}
		return nil
	}
	switch frame.Type {
	case FrameData:
		s.receiveDataLocked(st, frame.Payload)
	case FrameEnd:
		if st.gatewayEnded {
			return protocolError("second END on stream %d", st.id)
		}
		st.gatewayEnded = true
		st.cond.Broadcast()
		s.finishIfEndedLocked(st)
	case FrameReset:
		s.dropLocked(st)
	case FrameWindow:
		credit, _ := WindowCredit(frame)
		if st.sendWindow+int64(credit) > MaxWindow {
			return protocolError("WINDOW %d on stream %d exceeds the maximum window", credit, st.id)
		}
		st.sendWindow += int64(credit)
		st.cond.Broadcast()
	}
	return nil
}

func (s *Session) openLocked(frame Frame) error {
	if frame.StreamID <= s.highest {
		return protocolError("OPEN for stream %d is not above the highest opened id %d", frame.StreamID, s.highest)
	}
	request, err := DecodeOpen(frame.Payload)
	if err != nil {
		return err
	}
	s.highest = frame.StreamID
	if s.done {
		return nil
	}
	if len(s.streams) >= StreamsPerTunnel || s.perPreview[request.PreviewID] >= StreamsPerPreview {
		s.sendLocked(resetFrame(frame.StreamID, ResetLimit))
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	st := &stream{
		id:         frame.StreamID,
		previewID:  request.PreviewID,
		ctx:        ctx,
		cancel:     cancel,
		cond:       sync.NewCond(&s.mu),
		sendWindow: InitialWindow,
		recvWindow: InitialWindow,
		open:       true,
		lastData:   s.now(),
	}
	s.streams[st.id] = st
	s.live[st.id] = st
	s.perPreview[st.previewID]++
	s.wg.Add(1)
	go s.serve(st, request)
	return nil
}

func (s *Session) receiveDataLocked(st *stream, payload []byte) {
	if st.gatewayEnded {
		s.resetLocked(st, ResetProtocol)
		return
	}
	size := int64(len(payload))
	if size > st.recvWindow {
		s.resetLocked(st, ResetProtocol)
		return
	}
	st.recvWindow -= size
	st.queue = append(st.queue, bytes.Clone(payload))
	st.queued += size
	s.unconsumed += size
	st.lastData = s.now()
	st.cond.Broadcast()
}

func (s *Session) resetLocked(st *stream, code ResetCode) {
	if st.closed {
		return
	}
	if st.open {
		s.sendLocked(resetFrame(st.id, code))
	}
	s.dropLocked(st)
}

func (s *Session) closeProtocolLocked(st *stream) {
	if !st.open {
		return
	}
	st.open = false
	delete(s.streams, st.id)
	s.perPreview[st.previewID]--
	if s.perPreview[st.previewID] == 0 {
		delete(s.perPreview, st.previewID)
	}
}

func (s *Session) dropLocked(st *stream) {
	if st.closed {
		return
	}
	s.closeProtocolLocked(st)
	st.closed = true
	st.cancel()
	st.cond.Broadcast()
	queued := st.queued
	st.queue = nil
	st.queued = 0
	s.releaseLocked(queued)
	if st.upstream != nil {
		s.toClose = append(s.toClose, st.upstream)
	}
}

func (s *Session) finishIfEndedLocked(st *stream) {
	if st.gatewayEnded && st.runnerEnded {
		s.closeProtocolLocked(st)
	}
}

func (s *Session) releaseLocked(size int64) {
	before := s.unconsumed
	s.unconsumed -= size
	if before < TunnelCreditCap || s.unconsumed >= TunnelCreditCap {
		return
	}
	ids := make([]uint32, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	sortIDs(ids)
	for _, id := range ids {
		s.grantLocked(s.streams[id])
	}
}

func (s *Session) grantLocked(st *stream) {
	if !st.open || st.closed || st.gatewayEnded || st.ungranted < creditGrantThreshold || s.unconsumed >= TunnelCreditCap {
		return
	}
	s.sendLocked(windowFrame(st.id, st.ungranted))
	st.recvWindow += st.ungranted
	st.ungranted = 0
}

func (s *Session) serve(st *stream, request OpenRequest) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.live, st.id)
		s.mu.Unlock()
	}()
	address, ok := s.resolver.Resolve(st.ctx, request)
	if !ok {
		s.reset(st, ResetNotFound)
		return
	}
	upstream, err := s.dial(st.ctx, address)
	if err != nil {
		s.logger.Debug("preview tunnel dial failed", "stream_id", st.id, "preview_id", st.previewID, "error", err)
		s.reset(st, ResetUpstreamError)
		return
	}
	s.mu.Lock()
	if st.closed {
		s.mu.Unlock()
		_ = upstream.Close()
		return
	}
	st.upstream = upstream
	s.mu.Unlock()
	writerDone := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(writerDone)
		s.copyToUpstream(st)
	}()
	s.copyFromUpstream(st)
	<-writerDone
	_ = upstream.Close()
}

func (s *Session) reset(st *stream, code ResetCode) {
	s.mu.Lock()
	s.resetLocked(st, code)
	s.unlockAndClose()
}

func (s *Session) copyFromUpstream(st *stream) {
	buffer := make([]byte, MaxPayload)
	for {
		s.mu.Lock()
		for st.sendWindow == 0 && !st.closed {
			st.cond.Wait()
		}
		if st.closed {
			s.mu.Unlock()
			return
		}
		size := min(st.sendWindow, MaxPayload)
		s.mu.Unlock()
		n, err := st.upstream.Read(buffer[:size])
		s.mu.Lock()
		if st.closed {
			s.mu.Unlock()
			return
		}
		if n > 0 {
			st.sendWindow -= int64(n)
			st.lastData = s.now()
			s.sendLocked(Frame{Type: FrameData, StreamID: st.id, Payload: buffer[:n]})
		}
		if errors.Is(err, io.EOF) {
			st.runnerEnded = true
			s.sendLocked(Frame{Type: FrameEnd, StreamID: st.id})
			s.finishIfEndedLocked(st)
			s.mu.Unlock()
			return
		}
		if err != nil {
			s.logger.Debug("preview tunnel upstream read failed", "stream_id", st.id, "error", err)
			s.resetLocked(st, ResetUpstreamError)
			s.unlockAndClose()
			return
		}
		s.mu.Unlock()
	}
}

func (s *Session) copyToUpstream(st *stream) {
	for {
		s.mu.Lock()
		for len(st.queue) == 0 && !st.gatewayEnded && !st.closed {
			st.cond.Wait()
		}
		if st.closed {
			s.mu.Unlock()
			return
		}
		if len(st.queue) == 0 {
			s.mu.Unlock()
			if writer, ok := st.upstream.(closeWriter); ok {
				_ = writer.CloseWrite()
			}
			return
		}
		chunk := st.queue[0]
		st.queue[0] = nil
		st.queue = st.queue[1:]
		s.mu.Unlock()
		_, err := st.upstream.Write(chunk)
		s.mu.Lock()
		if st.closed {
			s.mu.Unlock()
			return
		}
		if err != nil {
			s.logger.Debug("preview tunnel upstream write failed", "stream_id", st.id, "error", err)
			s.resetLocked(st, ResetUpstreamError)
			s.unlockAndClose()
			return
		}
		size := int64(len(chunk))
		st.queued -= size
		st.ungranted += size
		s.releaseLocked(size)
		s.grantLocked(st)
		s.mu.Unlock()
	}
}

func (s *Session) shutdown() {
	s.mu.Lock()
	s.done = true
	s.outCond.Broadcast()
	for _, st := range s.live {
		s.dropLocked(st)
	}
	s.unlockAndClose()
}
