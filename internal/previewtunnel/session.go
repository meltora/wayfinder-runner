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
	CloseOutboxOverflow  = 1008
	OutboxDataLimit      = 4 << 20
	OutboxControlLimit   = 1 << 20
	creditGrantThreshold = InitialWindow / 2
	idleSweepInterval    = 15 * time.Second
	protocolCloseReason  = "protocol error"
	outboxOverflowReason = "outbox overflow"
)

var ErrOutboxOverflow = errors.New("preview tunnel outbox overflow")

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
	conn         Conn
	resolver     Resolver
	dial         DialFunc
	logger       *slog.Logger
	now          func() time.Time
	idleTicks    <-chan time.Time
	controlLimit int64

	mu            sync.Mutex
	outCond       *sync.Cond
	outbox        []outboundMessage
	outboxData    int64
	outboxControl int64
	ctx           context.Context
	cancel        context.CancelFunc
	done          bool
	overflowed    bool
	writeErr      error
	streams       map[uint32]*stream
	live          map[uint32]*stream
	perPreview    map[string]int
	highest       uint32
	unconsumed    int64
	toClose       []net.Conn
	wg            sync.WaitGroup
}

type outboundMessage struct {
	message []byte
	data    bool
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
		conn:         conn,
		resolver:     resolver,
		dial:         dial,
		logger:       logger,
		now:          now,
		controlLimit: OutboxControlLimit,
		streams:      map[uint32]*stream{},
		live:         map[uint32]*stream{},
		perPreview:   map[string]int{},
	}
	s.outCond = sync.NewCond(&s.mu)
	return s
}

func (s *Session) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s.mu.Lock()
	s.ctx = ctx
	s.cancel = cancel
	s.mu.Unlock()
	s.wg.Add(2)
	go s.writeLoop(ctx)
	go s.idleLoop(ctx)
	err := s.readLoop(ctx)
	s.shutdown()
	s.mu.Lock()
	overflowed := s.overflowed
	s.mu.Unlock()
	switch {
	case errors.Is(err, ErrProtocol):
		_ = s.conn.Close(CloseProtocolError, protocolCloseReason)
	case overflowed:
		_ = s.conn.Close(CloseOutboxOverflow, outboxOverflowReason)
		err = ErrOutboxOverflow
	}
	cancel()
	s.wg.Wait()
	s.mu.Lock()
	writeErr := s.writeErr
	s.mu.Unlock()
	switch {
	case errors.Is(err, ErrProtocol) || overflowed:
		return err
	case parent.Err() != nil:
		return parent.Err()
	case isContextError(err) && writeErr != nil && !isContextError(writeErr):
		return writeErr
	}
	return err
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Session) ResetPreview(previewID string) {
	s.mu.Lock()
	ids := make([]uint32, 0)
	for id, st := range s.live {
		if st.previewID == previewID {
			ids = append(ids, id)
		}
	}
	sortIDs(ids)
	for _, id := range ids {
		s.resetLocked(s.live[id], ResetNotFound)
	}
	s.unlockAndClose()
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
	if s.done || s.overflowed {
		return
	}
	message := EncodeFrame(frame)
	size := int64(len(message))
	data := frame.Type == FrameData
	if data {
		s.outboxData += size
	} else {
		if s.outboxControl+size > s.controlLimit {
			s.overflowed = true
			if cancel := s.cancel; cancel != nil {
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					_ = s.conn.Close(CloseOutboxOverflow, outboxOverflowReason)
					cancel()
				}()
			}
			return
		}
		s.outboxControl += size
	}
	s.outbox = append(s.outbox, outboundMessage{message: message, data: data})
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

func (s *Session) writeLoop(ctx context.Context) {
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
		next := s.outbox[0]
		s.outbox[0] = outboundMessage{}
		s.outbox = s.outbox[1:]
		size := int64(len(next.message))
		if next.data {
			before := s.outboxData
			s.outboxData -= size
			if before >= OutboxDataLimit && s.outboxData < OutboxDataLimit {
				for _, st := range s.live {
					st.cond.Broadcast()
				}
			}
		} else {
			s.outboxControl -= size
		}
		s.mu.Unlock()
		if err := s.conn.WriteMessage(ctx, next.message); err != nil {
			s.mu.Lock()
			if s.writeErr == nil {
				s.writeErr = err
			}
			cancel := s.cancel
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
	for id, st := range s.live {
		if !st.closed && now.Sub(st.lastData) >= IdleTimeout {
			ids = append(ids, id)
		}
	}
	sortIDs(ids)
	for _, id := range ids {
		s.resetLocked(s.live[id], ResetIdle)
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
	if len(s.live) >= StreamsPerTunnel || s.perPreview[request.PreviewID] >= StreamsPerPreview {
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
	defer s.release(st)
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

func (s *Session) release(st *stream) {
	s.mu.Lock()
	s.dropLocked(st)
	delete(s.live, st.id)
	s.perPreview[st.previewID]--
	if s.perPreview[st.previewID] <= 0 {
		delete(s.perPreview, st.previewID)
	}
	s.unlockAndClose()
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
		for (st.sendWindow == 0 || s.outboxData >= OutboxDataLimit) && !st.closed {
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
		_ = st.upstream.SetWriteDeadline(time.Now().Add(IdleTimeout))
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
