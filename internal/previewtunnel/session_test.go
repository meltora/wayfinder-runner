package previewtunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

const waitTimeout = 5 * time.Second

var errFakeConnClosed = errors.New("fake conn closed")

type fakeConn struct {
	in       chan []byte
	out      chan []byte
	closed   chan struct{}
	reuse    bool
	buffer   []byte
	mu       sync.Mutex
	reads    int
	code     int
	reason   string
	closeOne sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan []byte), out: make(chan []byte, 4096), closed: make(chan struct{})}
}

func (c *fakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	select {
	case message, ok := <-c.in:
		if !ok {
			return nil, io.EOF
		}
		if !c.reuse {
			return bytes.Clone(message), nil
		}
		if cap(c.buffer) < len(message) {
			c.buffer = make([]byte, MaxMessage)
		}
		c.buffer = c.buffer[:len(message)]
		copy(c.buffer, message)
		return c.buffer, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errFakeConnClosed
	}
}

func (c *fakeConn) WriteMessage(ctx context.Context, message []byte) error {
	select {
	case c.out <- bytes.Clone(message):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errFakeConnClosed
	}
}

func (c *fakeConn) Close(code int, reason string) error {
	c.closeOne.Do(func() {
		c.mu.Lock()
		c.code = code
		c.reason = reason
		c.mu.Unlock()
		close(c.closed)
	})
	return nil
}

func (c *fakeConn) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func (c *fakeConn) closeCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type halfConn struct {
	reader *io.PipeReader
	writer *io.PipeWriter
}

func duplexPipe() (*halfConn, *halfConn) {
	leftReader, rightWriter := io.Pipe()
	rightReader, leftWriter := io.Pipe()
	return &halfConn{reader: leftReader, writer: leftWriter}, &halfConn{reader: rightReader, writer: rightWriter}
}

func (c *halfConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *halfConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *halfConn) CloseWrite() error           { return c.writer.Close() }
func (c *halfConn) Close() error {
	c.writer.Close()
	c.reader.CloseWithError(net.ErrClosed)
	return nil
}
func (c *halfConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *halfConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *halfConn) SetDeadline(time.Time) error      { return nil }
func (c *halfConn) SetReadDeadline(time.Time) error  { return nil }
func (c *halfConn) SetWriteDeadline(time.Time) error { return nil }

type fakeResolver struct {
	previews map[string]fakePreview
}

type fakePreview struct {
	key     string
	address string
}

func (r *fakeResolver) Resolve(_ context.Context, request OpenRequest) (string, bool) {
	preview, ok := r.previews[request.PreviewID]
	if !ok || preview.key != request.PreviewKey {
		return "", false
	}
	return preview.address, true
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type harness struct {
	t         *testing.T
	conn      *fakeConn
	session   *Session
	upstreams chan *halfConn
	dialed    chan string
	ticks     chan time.Time
	clock     *fakeClock
	cancel    context.CancelFunc
	result    chan error
	dialErr   error
	probeID   uint32
}

func newHarness(t *testing.T, previews ...string) *harness {
	t.Helper()
	resolver := &fakeResolver{previews: map[string]fakePreview{}}
	for index, preview := range previews {
		resolver.previews[preview] = fakePreview{key: "key_" + preview, address: fmt.Sprintf("127.0.0.1:%d", 10080+index)}
	}
	h := &harness{
		t:         t,
		conn:      newFakeConn(),
		upstreams: make(chan *halfConn, 1024),
		dialed:    make(chan string, 1024),
		ticks:     make(chan time.Time),
		clock:     &fakeClock{now: time.Unix(1_800_000_000, 0)},
		result:    make(chan error, 1),
		probeID:   0x70000001,
	}
	dial := func(_ context.Context, address string) (net.Conn, error) {
		h.dialed <- address
		if h.dialErr != nil {
			return nil, h.dialErr
		}
		local, remote := duplexPipe()
		h.upstreams <- remote
		return local, nil
	}
	h.session = NewSession(h.conn, resolver, dial, nil, h.clock.Now)
	h.session.idleTicks = h.ticks
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.result <- h.session.Run(ctx) }()
	waitFor(h.t, "the session to start reading", func() bool { return h.conn.readCount() > 0 })
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-h.result:
		case <-time.After(waitTimeout):
			h.t.Error("session did not stop")
		}
	})
}

func (h *harness) deliver(frameType FrameType, streamID uint32, payload []byte) {
	h.t.Helper()
	h.deliverMessage(rawFrame(byte(frameType), streamID, payload))
}

func (h *harness) deliverMessage(message []byte) {
	h.t.Helper()
	before := h.conn.readCount()
	select {
	case h.conn.in <- message:
	case <-time.After(waitTimeout):
		h.t.Fatal("session is not reading")
	}
	waitFor(h.t, "the session to process a message", func() bool { return h.conn.readCount() > before })
}

func (h *harness) deliverFinal(frameType FrameType, streamID uint32, payload []byte) {
	h.t.Helper()
	select {
	case h.conn.in <- rawFrame(byte(frameType), streamID, payload):
	case <-time.After(waitTimeout):
		h.t.Fatal("session is not reading")
	}
}

func (h *harness) open(streamID uint32, preview string) {
	h.t.Helper()
	h.deliver(FrameOpen, streamID, []byte(`{"preview_id":"`+preview+`","preview_key":"key_`+preview+`"}`))
}

func (h *harness) openUpstream(streamID uint32, preview string) *halfConn {
	h.t.Helper()
	h.open(streamID, preview)
	return h.nextUpstream()
}

func (h *harness) nextUpstream() *halfConn {
	h.t.Helper()
	select {
	case upstream := <-h.upstreams:
		return upstream
	case <-time.After(waitTimeout):
		h.t.Fatal("no upstream dialed")
		return nil
	}
}

func (h *harness) next() Frame {
	h.t.Helper()
	select {
	case message := <-h.conn.out:
		frame, err := DecodeFrame(message)
		if err != nil {
			h.t.Fatalf("runner sent an invalid frame %x: %v", message, err)
		}
		return frame
	case <-time.After(waitTimeout):
		h.t.Fatal("no frame from the runner")
		return Frame{}
	}
}

func (h *harness) expectFrame(frameType FrameType, streamID uint32, payload []byte) {
	h.t.Helper()
	frame := h.next()
	if frame.Type != frameType || frame.StreamID != streamID || !bytes.Equal(frame.Payload, payload) {
		h.t.Fatalf("runner sent type %d stream %d payload %x, want type %d stream %d payload %x", frame.Type, frame.StreamID, frame.Payload, frameType, streamID, payload)
	}
}

func (h *harness) expectReset(streamID uint32, code ResetCode) {
	h.t.Helper()
	h.expectFrame(FrameReset, streamID, []byte{byte(code)})
}

func (h *harness) expectQuiet() {
	h.t.Helper()
	h.expectQuietFor(20 * time.Millisecond)
}

func (h *harness) expectQuietFor(settle time.Duration) {
	h.t.Helper()
	time.Sleep(settle)
	select {
	case message := <-h.conn.out:
		h.t.Fatalf("runner sent an unexpected frame %x", message)
	default:
	}
}

func (h *harness) expectAlive() {
	h.t.Helper()
	id := h.probeID
	h.probeID += 2
	h.deliver(FrameOpen, id, []byte(`{"preview_id":"probe","preview_key":"probe"}`))
	h.expectReset(id, ResetNotFound)
}

func (h *harness) expectClosed1002() {
	h.t.Helper()
	select {
	case err := <-h.result:
		if !errors.Is(err, ErrProtocol) {
			h.t.Fatalf("Run error = %v, want ErrProtocol", err)
		}
		h.result <- err
	case <-time.After(waitTimeout):
		h.t.Fatal("session did not close")
	}
	if code := h.conn.closeCode(); code != 1002 {
		h.t.Fatalf("close code = %d, want 1002", code)
	}
}

func (h *harness) tick() {
	h.t.Helper()
	for range 2 {
		select {
		case h.ticks <- h.clock.Now():
		case <-time.After(waitTimeout):
			h.t.Fatal("idle loop is not running")
		}
	}
}

func expectUpstreamClosed(t *testing.T, upstream *halfConn) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := upstream.Write([]byte("probe"))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the runner still reads the upstream, want closed")
		}
	case <-time.After(waitTimeout):
		t.Fatal("upstream was not closed")
	}
}

func readUpstream(t *testing.T, upstream *halfConn, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(upstream, data)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read upstream: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("upstream received too little")
	}
	return data
}

func creditFrame(credit uint32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, credit)
	return payload
}

func TestOpenUnknownPreviewResetsNotFound(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	h.deliver(FrameOpen, 1, []byte(`{"preview_id":"prv_2","preview_key":"key_prv_2"}`))
	h.expectReset(1, ResetNotFound)
	h.deliver(FrameOpen, 3, []byte(`{"preview_id":"prv_1","preview_key":"wrong"}`))
	h.expectReset(3, ResetNotFound)
	h.expectQuiet()
	if len(h.dialed) != 0 {
		t.Fatal("an unknown preview was dialed")
	}
}

func TestOpenResolvesFromRegistryNotPayload(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	h.open(1, "prv_1")
	select {
	case address := <-h.dialed:
		if address != "127.0.0.1:10080" {
			t.Fatalf("dialed %q, want the resolver address", address)
		}
	case <-time.After(waitTimeout):
		t.Fatal("not dialed")
	}
}

func TestDialFailureResetsUpstreamError(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.dialErr = errors.New("connection refused")
	h.start()
	h.open(1, "prv_1")
	h.expectReset(1, ResetUpstreamError)
	h.expectAlive()
}

func TestBytesFlowBothWaysAndEndHalfCloses(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	h.deliver(FrameData, 1, []byte("ping"))
	if got := readUpstream(t, upstream, 4); string(got) != "ping" {
		t.Fatalf("upstream got %q", got)
	}
	go func() { _, _ = upstream.Write([]byte("pong")) }()
	h.expectFrame(FrameData, 1, []byte("pong"))
	h.deliver(FrameEnd, 1, nil)
	rest, err := io.ReadAll(upstream)
	if err != nil || len(rest) != 0 {
		t.Fatalf("upstream after END: %q, %v, want a clean EOF", rest, err)
	}
	if err := upstream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	h.expectFrame(FrameEnd, 1, nil)
	h.deliver(FrameData, 1, []byte("late"))
	h.expectQuiet()
	h.expectAlive()
}

func TestSenderRespectsWindow(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	written := make(chan struct{})
	go func() {
		_, _ = upstream.Write(bytes.Repeat([]byte{'z'}, 1<<20))
		close(written)
	}()
	sent := 0
	for sent < InitialWindow {
		frame := h.next()
		if frame.Type != FrameData || frame.StreamID != 1 {
			t.Fatalf("unexpected frame type %d stream %d", frame.Type, frame.StreamID)
		}
		if len(frame.Payload) > MaxPayload {
			t.Fatalf("DATA of %d bytes exceeds MaxPayload", len(frame.Payload))
		}
		sent += len(frame.Payload)
	}
	if sent != InitialWindow {
		t.Fatalf("sent %d bytes, want exactly %d", sent, InitialWindow)
	}
	h.expectQuiet()
	select {
	case <-written:
		t.Fatal("the upstream write completed while the window was 0")
	default:
	}
	h.deliver(FrameWindow, 1, creditFrame(1000))
	frame := h.next()
	if frame.Type != FrameData || len(frame.Payload) != 1000 {
		t.Fatalf("after WINDOW 1000 got type %d with %d bytes", frame.Type, len(frame.Payload))
	}
	h.expectQuiet()
}

func TestDataBeyondWindowResetsProtocol(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	for range InitialWindow / MaxPayload {
		h.deliver(FrameData, 1, bytes.Repeat([]byte{'a'}, MaxPayload))
	}
	h.expectQuiet()
	h.deliver(FrameData, 1, []byte("b"))
	h.expectReset(1, ResetProtocol)
	expectUpstreamClosed(t, upstream)
	h.expectAlive()
}

func TestDataAfterOwnEndResetsProtocolOnly(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	h.deliver(FrameEnd, 1, nil)
	h.deliver(FrameData, 1, []byte("x"))
	h.expectReset(1, ResetProtocol)
	expectUpstreamClosed(t, upstream)
	h.expectAlive()
}

func TestSecondEndClosesTunnel1002(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	h.openUpstream(1, "prv_1")
	h.deliver(FrameEnd, 1, nil)
	h.deliverFinal(FrameEnd, 1, nil)
	h.expectClosed1002()
}

func TestNonIncreasingOpenCloses1002(t *testing.T) {
	for _, second := range []uint32{3, 1} {
		h := newHarness(t, "prv_1")
		h.start()
		h.openUpstream(3, "prv_1")
		h.deliverFinal(FrameOpen, second, []byte(`{"preview_id":"prv_1","preview_key":"key_prv_1"}`))
		h.expectClosed1002()
	}
}

func TestFrameAboveHighestOpenedIdCloses1002(t *testing.T) {
	cases := []struct {
		name    string
		opened  bool
		frame   FrameType
		payload []byte
	}{
		{"data_before_any_open", false, FrameData, []byte("x")},
		{"data", true, FrameData, []byte("x")},
		{"end", true, FrameEnd, nil},
		{"window", true, FrameWindow, creditFrame(1)},
		{"reset", true, FrameReset, []byte{byte(ResetRefused)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "prv_1")
			h.start()
			if tc.opened {
				h.openUpstream(1, "prv_1")
			}
			h.deliverFinal(tc.frame, 3, tc.payload)
			h.expectClosed1002()
		})
	}
}

func TestInvalidFrameCloses1002(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	h.deliverFinal(FrameData, 2, []byte("x"))
	h.expectClosed1002()
}

func TestFrameForClosedStreamIsDropped(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	h.open(1, "nope")
	h.expectReset(1, ResetNotFound)
	h.openUpstream(5, "prv_1")
	h.deliver(FrameReset, 5, []byte{byte(ResetRefused)})
	for _, id := range []uint32{1, 3, 5} {
		h.deliver(FrameData, id, []byte("x"))
		h.deliver(FrameEnd, id, nil)
		h.deliver(FrameEnd, id, nil)
		h.deliver(FrameWindow, id, creditFrame(MaxWindow))
		h.deliver(FrameReset, id, []byte{byte(ResetIdle)})
	}
	h.expectQuiet()
	h.expectAlive()
}

func TestUnknownResetCodeClosesStreamOnly(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	h.deliver(FrameReset, 1, []byte{7})
	expectUpstreamClosed(t, upstream)
	h.deliver(FrameData, 1, []byte("x"))
	h.expectQuiet()
	h.expectAlive()
}

func TestStreamLimitsResetLimit(t *testing.T) {
	previews := []string{"a", "b", "c", "d", "e"}
	h := newHarness(t, previews...)
	h.start()
	id := uint32(1)
	for range StreamsPerPreview {
		h.open(id, "a")
		id += 2
	}
	h.open(id, "a")
	h.expectReset(id, ResetLimit)
	id += 2
	for _, preview := range previews[1:4] {
		for range StreamsPerPreview {
			h.open(id, preview)
			id += 2
		}
	}
	h.open(id, "e")
	h.expectReset(id, ResetLimit)
	id += 2
	first := uint32(1)
	h.deliver(FrameReset, first, []byte{byte(ResetRefused)})
	h.open(id, "e")
	h.expectQuiet()
	id += 2
	h.open(id, "a")
	h.expectReset(id, ResetLimit)
}

func TestIdleStreamResetsIdle(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	go func() { _, _ = io.Copy(io.Discard, upstream) }()
	h.clock.Advance(4 * time.Minute)
	h.tick()
	h.deliver(FrameData, 1, []byte("hmr"))
	h.clock.Advance(4 * time.Minute)
	h.tick()
	h.expectAlive()
	h.clock.Advance(59 * time.Second)
	h.tick()
	h.expectAlive()
	h.clock.Advance(time.Second)
	h.tick()
	h.expectReset(1, ResetIdle)
	h.deliver(FrameData, 1, []byte("x"))
	h.expectQuiet()
}

func TestIdleTimerCountsRunnerData(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	h.clock.Advance(4 * time.Minute)
	go func() { _, _ = upstream.Write([]byte("tick")) }()
	h.expectFrame(FrameData, 1, []byte("tick"))
	h.clock.Advance(4 * time.Minute)
	h.tick()
	h.expectAlive()
	h.clock.Advance(time.Minute)
	h.tick()
	h.expectReset(1, ResetIdle)
}

func TestWindowGrantOverflowCloses1002(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	h.openUpstream(1, "prv_1")
	h.deliver(FrameWindow, 1, creditFrame(MaxWindow-InitialWindow))
	h.expectQuiet()
	h.deliverFinal(FrameWindow, 1, creditFrame(1))
	h.expectClosed1002()
}

func TestWindowGrantedOnlyAfterUpstreamConsumes(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	h.deliver(FrameData, 1, bytes.Repeat([]byte{'a'}, MaxPayload))
	h.deliver(FrameData, 1, bytes.Repeat([]byte{'b'}, MaxPayload))
	h.expectQuiet()
	readUpstream(t, upstream, MaxPayload)
	h.expectQuiet()
	readUpstream(t, upstream, MaxPayload)
	h.expectFrame(FrameWindow, 1, creditFrame(2*MaxPayload))
}

func TestCreditWithheldAtTunnelCap(t *testing.T) {
	h := newHarness(t, "a", "b")
	h.start()
	var upstreams []*halfConn
	id := uint32(1)
	for index := range StreamsPerPreview + 1 {
		preview := "a"
		if index == StreamsPerPreview {
			preview = "b"
		}
		upstreams = append(upstreams, h.openUpstream(id, preview))
		for range InitialWindow / MaxPayload {
			h.deliver(FrameData, id, bytes.Repeat([]byte{byte(index)}, MaxPayload))
		}
		id += 2
	}
	h.expectQuiet()
	readUpstream(t, upstreams[0], InitialWindow)
	h.expectQuiet()
	readUpstream(t, upstreams[1], MaxPayload)
	h.expectFrame(FrameWindow, 1, creditFrame(InitialWindow))
	h.expectQuiet()
}

func TestResetPreviewResetsAllItsStreams(t *testing.T) {
	h := newHarness(t, "a", "b")
	h.start()
	first := h.openUpstream(1, "a")
	other := h.openUpstream(3, "b")
	second := h.openUpstream(5, "a")
	h.session.ResetPreview("a")
	h.expectReset(1, ResetNotFound)
	h.expectReset(5, ResetNotFound)
	expectUpstreamClosed(t, first)
	expectUpstreamClosed(t, second)
	h.expectQuiet()
	h.deliver(FrameData, 3, []byte("still"))
	if got := readUpstream(t, other, 5); string(got) != "still" {
		t.Fatalf("other preview got %q", got)
	}
	h.deliver(FrameData, 1, []byte("x"))
	h.expectQuiet()
}

func TestConnBufferOwnership(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		h := newHarness(t, "prv_1")
		h.conn.reuse = reuse
		h.start()
		upstream := h.openUpstream(1, "prv_1")
		h.deliver(FrameData, 1, []byte("aaaa"))
		h.deliver(FrameData, 1, []byte("bbbb"))
		h.deliver(FrameWindow, 1, creditFrame(1))
		if got := readUpstream(t, upstream, 8); string(got) != "aaaabbbb" {
			t.Fatalf("reuse=%v: upstream got %q", reuse, got)
		}
	}
}

func TestRunExitClosesUpstreamsAndLeaksNoGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()
	h := newHarness(t, "prv_1")
	h.start()
	var upstreams []*halfConn
	for id := uint32(1); id <= 9; id += 2 {
		upstreams = append(upstreams, h.openUpstream(id, "prv_1"))
	}
	h.deliver(FrameData, 1, []byte("queued"))
	h.deliver(FrameEnd, 3, nil)
	h.open(11, "nope")
	h.expectReset(11, ResetNotFound)
	close(h.conn.in)
	select {
	case err := <-h.result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Run error = %v, want io.EOF", err)
		}
		h.result <- err
	case <-time.After(waitTimeout):
		t.Fatal("session did not stop")
	}
	for _, upstream := range upstreams {
		expectUpstreamClosed(t, upstream)
	}
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline })
}

func TestConnFailureStopsSession(t *testing.T) {
	h := newHarness(t, "prv_1")
	h.start()
	upstream := h.openUpstream(1, "prv_1")
	h.conn.Close(1000, "gone")
	select {
	case err := <-h.result:
		if err == nil {
			t.Fatal("Run returned nil")
		}
		h.result <- err
	case <-time.After(waitTimeout):
		t.Fatal("session did not stop")
	}
	expectUpstreamClosed(t, upstream)
}
