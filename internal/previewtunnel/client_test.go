package previewtunnel

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestTunnelURLFromControlURL(t *testing.T) {
	cases := map[string]string{
		"wss://h/v1/runners/stream?x=1":         "wss://h/v1/runners/preview-tunnel",
		"wss://h/runner":                        "wss://h/v1/runners/preview-tunnel",
		"wss://h:8443/v1/runners/stream#frag":   "wss://h:8443/v1/runners/preview-tunnel",
		"ws://127.0.0.1:9000/x?":                "ws://127.0.0.1:9000/v1/runners/preview-tunnel",
		"wss://user:secret@h/v1/runners/stream": "wss://h/v1/runners/preview-tunnel",
	}
	for control, want := range cases {
		got, err := TunnelURL(control)
		if err != nil || got != want {
			t.Fatalf("TunnelURL(%q) = %q, %v; want %q", control, got, err, want)
		}
	}
	for _, invalid := range []string{"", "h/v1/runners/stream", "ftp://h/x", "https://h/x", "http://h/x", "wss:///x", "wss://:443/x", "://bad"} {
		if got, err := TunnelURL(invalid); err == nil {
			t.Fatalf("TunnelURL(%q) = %q, want an error", invalid, got)
		}
	}
}

type tunnelServer struct {
	t       *testing.T
	server  *httptest.Server
	handler func(w http.ResponseWriter, r *http.Request)
}

func newTunnelServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *tunnelServer {
	t.Helper()
	ts := &tunnelServer{t: t, handler: handler}
	ts.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != TunnelPath || r.URL.RawQuery != "" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		ts.handler(w, r)
	}))
	ts.server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	ts.server.StartTLS()
	t.Cleanup(ts.server.Close)
	return ts
}

func (ts *tunnelServer) clientTLS(commonName string) *tls.Config {
	ts.t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(ts.server.Certificate())
	return &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{clientCertificate(ts.t, commonName)}, MinVersion: tls.VersionTLS12}
}

func clientCertificate(t *testing.T, commonName string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type backoffRecorder struct {
	mu    sync.Mutex
	bases []time.Duration
}

func (b *backoffRecorder) wait(base time.Duration) time.Duration {
	b.mu.Lock()
	b.bases = append(b.bases, base)
	b.mu.Unlock()
	return time.Millisecond
}

func (b *backoffRecorder) recorded() []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]time.Duration(nil), b.bases...)
}

func (ts *tunnelServer) tunnelURL() string {
	ts.t.Helper()
	url, err := TunnelURL("wss://" + ts.server.Listener.Addr().String() + "/v1/runners/stream")
	if err != nil {
		ts.t.Fatal(err)
	}
	return url
}

func (ts *tunnelServer) dialRaw() *websocket.Conn {
	ts.t.Helper()
	httpTransport := &http.Transport{TLSClientConfig: ts.clientTLS("runner-a")}
	ts.t.Cleanup(httpTransport.CloseIdleConnections)
	conn, _, err := websocket.Dial(context.Background(), ts.tunnelURL(), &websocket.DialOptions{HTTPClient: &http.Client{Transport: httpTransport}})
	if err != nil {
		ts.t.Fatal(err)
	}
	ts.t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func startClient(t *testing.T, ts *tunnelServer, resolver Resolver, configure func(*Client)) (*Client, *backoffRecorder) {
	t.Helper()
	client, recorder, stop := startStoppableClient(t, ts, resolver, configure)
	t.Cleanup(func() {
		if err := stop(); !errors.Is(err, context.Canceled) {
			t.Errorf("Run error = %v, want context.Canceled", err)
		}
	})
	return client, recorder
}

func startStoppableClient(t *testing.T, ts *tunnelServer, resolver Resolver, configure func(*Client)) (*Client, *backoffRecorder, func() error) {
	t.Helper()
	if resolver == nil {
		resolver = &fakeResolver{previews: map[string]fakePreview{}}
	}
	client := NewClient(ts.tunnelURL(), ts.clientTLS("runner-a"), resolver, nil)
	recorder := &backoffRecorder{}
	client.backoff = recorder.wait
	if configure != nil {
		configure(client)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Run(ctx) }()
	var once sync.Once
	var runErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case runErr = <-result:
			case <-time.After(waitTimeout):
				runErr = errors.New("client did not stop")
			}
		})
		return runErr
	}
	return client, recorder, stop
}

func receive[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func accept(t *testing.T, w http.ResponseWriter, r *http.Request) *websocket.Conn {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Errorf("accept: %v", err)
		return nil
	}
	conn.SetReadLimit(-1)
	return conn
}

func drain(conn *websocket.Conn) error {
	for {
		if _, _, err := conn.Read(context.Background()); err != nil {
			return err
		}
	}
}

func TestClientReconnectsAfterServerClose(t *testing.T) {
	accepted := make(chan struct{}, 16)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		accepted <- struct{}{}
		_ = conn.Close(websocket.StatusNormalClosure, "replaced")
	})
	_, recorder := startClient(t, ts, nil, nil)
	for range 4 {
		receive(t, "a tunnel connection", accepted)
	}
	got := recorder.recorded()[:3]
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("backoff bases = %v, want prefix %v", got, want)
		}
	}
}

func TestClientBackoffResetsAfterAMinuteConnected(t *testing.T) {
	accepted := make(chan struct{}, 16)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		accepted <- struct{}{}
		_ = conn.Close(websocket.StatusNormalClosure, "replaced")
	})
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	_, recorder := startClient(t, ts, nil, func(c *Client) {
		c.now = func() time.Time {
			clock.Advance(61 * time.Second)
			return clock.Now()
		}
	})
	for range 4 {
		receive(t, "a tunnel connection", accepted)
	}
	for _, base := range recorder.recorded()[:3] {
		if base != time.Second {
			t.Fatalf("backoff bases = %v, want every base at 1s", recorder.recorded())
		}
	}
}

func TestClientRetriesAfter403(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	accepted := make(chan struct{}, 1)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n <= 3 {
			http.Error(w, "tunnel not advertised", http.StatusForbidden)
			return
		}
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		accepted <- struct{}{}
		_ = drain(conn)
	})
	_, recorder := startClient(t, ts, nil, nil)
	receive(t, "the tunnel after 403s", accepted)
	got := recorder.recorded()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("backoff bases = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("backoff bases = %v, want %v", got, want)
		}
	}
}

func TestClientClosesOnTextMessage1003(t *testing.T) {
	statuses := make(chan websocket.StatusCode, 4)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		if err := conn.Write(context.Background(), websocket.MessageText, []byte("hello")); err != nil {
			t.Errorf("write text: %v", err)
			return
		}
		statuses <- websocket.CloseStatus(drain(conn))
	})
	startClient(t, ts, nil, nil)
	if status := receive(t, "the close status", statuses); status != CloseUnsupportedData {
		t.Fatalf("close status = %d, want 1003", status)
	}
}

func closeStatusFor(t *testing.T, message []byte) websocket.StatusCode {
	t.Helper()
	statuses := make(chan websocket.StatusCode, 4)
	var first sync.Once
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		isFirst := false
		first.Do(func() { isFirst = true })
		if !isFirst {
			_ = drain(conn)
			return
		}
		if err := conn.Write(context.Background(), websocket.MessageBinary, message); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		statuses <- websocket.CloseStatus(drain(conn))
	})
	startClient(t, ts, nil, nil)
	return receive(t, "the close status", statuses)
}

func TestOversizeMessageClosesTunnel(t *testing.T) {
	for _, size := range []int{MaxPayload + 1, 4 * MaxMessage} {
		message := rawFrame(byte(FrameData), 1, bytes.Repeat([]byte{'x'}, size))
		if status := closeStatusFor(t, message); status != CloseProtocolError {
			t.Fatalf("%d-byte message: close status = %d, want 1002", len(message), status)
		}
	}
}

func TestInvalidFrameClosesTunnel1002(t *testing.T) {
	if status := closeStatusFor(t, []byte{1, 0, 0, 0}); status != CloseProtocolError {
		t.Fatalf("close status = %d, want 1002", status)
	}
}

type gatedConn struct {
	Conn
}

func (g *gatedConn) WriteMessage(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestOutboxOverflowSends1008OverWebsocket(t *testing.T) {
	statuses := make(chan websocket.CloseError, 1)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		for id := uint32(1); id < 80; id += 2 {
			message := rawFrame(byte(FrameOpen), id, []byte(`{"preview_id":"x","preview_key":"y"}`))
			if err := conn.Write(context.Background(), websocket.MessageBinary, message); err != nil {
				break
			}
		}
		var closeErr websocket.CloseError
		_ = errors.As(drain(conn), &closeErr)
		statuses <- closeErr
	})
	ws := newWSConn(ts.dialRaw(), time.Second)
	session := NewSession(&gatedConn{Conn: ws}, &fakeResolver{previews: map[string]fakePreview{}}, nil, nil, nil)
	session.controlLimit = 60
	result := make(chan error, 1)
	go func() { result <- session.Run(context.Background()) }()
	closed := receive(t, "the server to see the close", statuses)
	if closed.Code != CloseOutboxOverflow || closed.Reason != outboxOverflowReason {
		t.Fatalf("server saw close %d %q, want 1008 %q", closed.Code, closed.Reason, outboxOverflowReason)
	}
	if err := receive(t, "the session to stop", result); !errors.Is(err, ErrOutboxOverflow) {
		t.Fatalf("Run error = %v, want ErrOutboxOverflow", err)
	}
}

func TestClientSaysGoodbyeOnShutdown(t *testing.T) {
	closes := make(chan websocket.CloseError, 1)
	accepted := make(chan struct{}, 1)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		ctx := context.Background()
		open := rawFrame(byte(FrameOpen), 1, []byte(`{"preview_id":"x","preview_key":"y"}`))
		if err := conn.Write(ctx, websocket.MessageBinary, open); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Errorf("read RESET: %v", err)
			return
		}
		accepted <- struct{}{}
		var closeErr websocket.CloseError
		_ = errors.As(drain(conn), &closeErr)
		closes <- closeErr
	})
	_, _, stop := startStoppableClient(t, ts, nil, nil)
	receive(t, "the tunnel", accepted)
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	closed := receive(t, "the goodbye", closes)
	if closed.Code != CloseNormal || closed.Reason != goodbyeReason {
		t.Fatalf("server saw close %d %q, want 1000 %q", closed.Code, closed.Reason, goodbyeReason)
	}
}

func TestClientShutdownIsBoundedWhenGatewayIsSilent(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		accepted <- conn
		<-release
		_ = conn.CloseNow()
	})
	t.Cleanup(func() { close(release) })
	_, _, stop := startStoppableClient(t, ts, nil, func(c *Client) { c.closeTimeout = 20 * time.Millisecond })
	receive(t, "the tunnel", accepted)
	started := time.Now()
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed >= 4*time.Second {
		t.Fatalf("Run took %s to return against a silent gateway", elapsed)
	}
}

func TestClientCarriesMaxSizeFramesToUpstream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	resolver := &fakeResolver{previews: map[string]fakePreview{"prv_1": {key: "key_prv_1", address: listener.Addr().String()}}}
	payload := bytes.Repeat([]byte{'p'}, MaxPayload)
	replies := make(chan Frame, 4)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		ctx := context.Background()
		open := rawFrame(byte(FrameOpen), 1, []byte(`{"preview_id":"prv_1","preview_key":"key_prv_1"}`))
		data := rawFrame(byte(FrameData), 1, payload)
		if len(data) != MaxMessage {
			t.Errorf("data message is %d bytes, want %d", len(data), MaxMessage)
		}
		for _, message := range [][]byte{open, data} {
			if err := conn.Write(ctx, websocket.MessageBinary, message); err != nil {
				t.Errorf("write: %v", err)
				return
			}
		}
		for {
			messageType, message, err := conn.Read(ctx)
			if err != nil {
				return
			}
			frame, err := DecodeFrame(message)
			if messageType != websocket.MessageBinary || err != nil {
				t.Errorf("runner sent %v %x: %v", messageType, message, err)
				return
			}
			replies <- frame
		}
	})
	startClient(t, ts, resolver, nil)
	upstreams := make(chan net.Conn, 1)
	go func() {
		upstream, err := listener.Accept()
		if err == nil {
			upstreams <- upstream
		}
	}()
	upstream := receive(t, "the upstream dial", upstreams)
	t.Cleanup(func() { _ = upstream.Close() })
	got := make([]byte, MaxPayload)
	if _, err := io.ReadFull(upstream, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("upstream read %d matching bytes: %v", len(got), err)
	}
	if _, err := upstream.Write([]byte("reply")); err != nil {
		t.Fatal(err)
	}
	frame := receive(t, "the runner DATA", replies)
	if frame.Type != FrameData || frame.StreamID != 1 || string(frame.Payload) != "reply" {
		t.Fatalf("runner frame = %+v, want DATA 1 \"reply\"", frame)
	}
}

func TestClientUpdateTLSConfigRedials(t *testing.T) {
	type session struct {
		commonName string
		closed     chan websocket.CloseError
	}
	sessions := make(chan session, 4)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		commonName := r.TLS.PeerCertificates[0].Subject.CommonName
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		s := session{commonName: commonName, closed: make(chan websocket.CloseError, 1)}
		sessions <- s
		var closeErr websocket.CloseError
		_ = errors.As(drain(conn), &closeErr)
		s.closed <- closeErr
	})
	client, _ := startClient(t, ts, nil, nil)
	first := receive(t, "the first tunnel", sessions)
	if first.commonName != "runner-a" {
		t.Fatalf("first tunnel certificate = %q, want runner-a", first.commonName)
	}
	client.UpdateTLSConfig(ts.clientTLS("runner-b"))
	closed := receive(t, "the old tunnel to close", first.closed)
	if closed.Code != websocket.StatusNormalClosure || closed.Reason != rotatedReason {
		t.Fatalf("old tunnel closed with %d %q", closed.Code, closed.Reason)
	}
	second := receive(t, "the redialed tunnel", sessions)
	if second.commonName != "runner-b" {
		t.Fatalf("redialed tunnel certificate = %q, want runner-b", second.commonName)
	}
}

func TestClientUpdateTLSConfigWhileDialingClosesCleanly(t *testing.T) {
	type session struct {
		commonName string
		closed     chan websocket.CloseError
	}
	sessions := make(chan session, 4)
	dialing := make(chan struct{}, 4)
	rotated := make(chan struct{})
	var once sync.Once
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		commonName := r.TLS.PeerCertificates[0].Subject.CommonName
		if commonName == "runner-a" {
			once.Do(func() {
				dialing <- struct{}{}
				<-rotated
			})
		}
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		s := session{commonName: commonName, closed: make(chan websocket.CloseError, 1)}
		sessions <- s
		var closeErr websocket.CloseError
		_ = errors.As(drain(conn), &closeErr)
		s.closed <- closeErr
	})
	client, _ := startClient(t, ts, nil, nil)
	receive(t, "the first dial", dialing)
	client.UpdateTLSConfig(ts.clientTLS("runner-b"))
	close(rotated)
	first := receive(t, "the first tunnel", sessions)
	if first.commonName != "runner-a" {
		t.Fatalf("first tunnel certificate = %q, want runner-a", first.commonName)
	}
	closed := receive(t, "the stale tunnel to close", first.closed)
	if closed.Code != websocket.StatusNormalClosure || closed.Reason != rotatedReason {
		t.Fatalf("stale tunnel closed with %d %q", closed.Code, closed.Reason)
	}
	second := receive(t, "the redialed tunnel", sessions)
	if second.commonName != "runner-b" {
		t.Fatalf("redialed tunnel certificate = %q, want runner-b", second.commonName)
	}
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestClientClosesWhenPongsStop(t *testing.T) {
	accepted := make(chan struct{}, 16)
	release := make(chan struct{})
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		accepted <- struct{}{}
		<-release
		_ = conn.CloseNow()
	})
	t.Cleanup(func() { close(release) })
	logs := &lockedBuffer{}
	startClient(t, ts, nil, func(c *Client) {
		c.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c.pingInterval = 10 * time.Millisecond
		c.pingTimeout = 50 * time.Millisecond
	})
	receive(t, "the first tunnel", accepted)
	receive(t, "the tunnel redialed after missing pongs", accepted)
	output := logs.String()
	if !strings.Contains(output, "preview tunnel ping failed") || !strings.Contains(output, "failed to wait for pong") {
		t.Fatalf("first tunnel did not end on the ping timeout; logs:\n%s", output)
	}
}

func TestClientUpdateTLSConfigDoesNotBlockOnSilentGateway(t *testing.T) {
	type session struct {
		commonName string
	}
	sessions := make(chan session, 4)
	release := make(chan struct{})
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		commonName := r.TLS.PeerCertificates[0].Subject.CommonName
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		sessions <- session{commonName: commonName}
		<-release
		_ = conn.CloseNow()
	})
	t.Cleanup(func() { close(release) })
	client, _ := startClient(t, ts, nil, func(c *Client) { c.closeTimeout = 20 * time.Millisecond })
	if first := receive(t, "the first tunnel", sessions); first.commonName != "runner-a" {
		t.Fatalf("first tunnel certificate = %q, want runner-a", first.commonName)
	}
	returned := make(chan struct{})
	go func() {
		client.UpdateTLSConfig(ts.clientTLS("runner-b"))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("UpdateTLSConfig blocked on a gateway that does not answer the close")
	}
	if second := receive(t, "the redialed tunnel", sessions); second.commonName != "runner-b" {
		t.Fatalf("redialed tunnel certificate = %q, want runner-b", second.commonName)
	}
}

func TestClientRotatedWhileDialingRedialsWithNewConfig(t *testing.T) {
	dialing := make(chan struct{}, 1)
	proceed := make(chan struct{})
	names := make(chan string, 4)
	var mu sync.Mutex
	requests := 0
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		first := requests == 1
		mu.Unlock()
		if first {
			dialing <- struct{}{}
			<-proceed
		}
		commonName := r.TLS.PeerCertificates[0].Subject.CommonName
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		names <- commonName
		_ = drain(conn)
	})
	client, _ := startClient(t, ts, nil, nil)
	receive(t, "the first dial", dialing)
	client.UpdateTLSConfig(ts.clientTLS("runner-b"))
	close(proceed)
	if name := receive(t, "the first upgrade", names); name != "runner-a" {
		t.Fatalf("first upgrade certificate = %q, want runner-a", name)
	}
	if name := receive(t, "the redial", names); name != "runner-b" {
		t.Fatalf("redial certificate = %q, want runner-b", name)
	}
}

func TestClientResetPreviewWithoutSessionIsNoop(t *testing.T) {
	client := NewClient("wss://127.0.0.1:1/v1/runners/preview-tunnel", nil, &fakeResolver{}, nil)
	client.ResetPreview("prv_1")
	if client.pingInterval != 20*time.Second || client.pingTimeout != 60*time.Second {
		t.Fatalf("keepalive = %s/%s, want 20s/60s", client.pingInterval, client.pingTimeout)
	}
}

func TestClientResetPreviewForwardsToLiveSession(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	resolver := &fakeResolver{previews: map[string]fakePreview{"prv_1": {key: "key_prv_1", address: listener.Addr().String()}}}
	frames := make(chan Frame, 4)
	ts := newTunnelServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn := accept(t, w, r)
		if conn == nil {
			return
		}
		ctx := context.Background()
		open := rawFrame(byte(FrameOpen), 1, []byte(`{"preview_id":"prv_1","preview_key":"key_prv_1"}`))
		if err := conn.Write(ctx, websocket.MessageBinary, open); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		for {
			_, message, err := conn.Read(ctx)
			if err != nil {
				return
			}
			frame, err := DecodeFrame(message)
			if err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			frames <- frame
		}
	})
	client, _ := startClient(t, ts, resolver, nil)
	upstreams := make(chan net.Conn, 1)
	go func() {
		upstream, err := listener.Accept()
		if err == nil {
			upstreams <- upstream
		}
	}()
	upstream := receive(t, "the upstream dial", upstreams)
	t.Cleanup(func() { _ = upstream.Close() })
	client.ResetPreview("prv_1")
	frame := receive(t, "the RESET", frames)
	if code, _, _ := ResetCodeOf(frame); frame.Type != FrameReset || frame.StreamID != 1 || code != ResetNotFound {
		t.Fatalf("frame = %+v, want RESET 1 not_found", frame)
	}
	if _, err := upstream.Read(make([]byte, 1)); err == nil {
		t.Fatal("upstream stayed open after ResetPreview")
	}
}
