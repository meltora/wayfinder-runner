package previewgateway

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type resolverFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

func (fn resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return fn(ctx, network, host)
}

type recordingResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr
	calls   []string
}

func (resolver *recordingResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls = append(resolver.calls, host)
	if len(resolver.answers) == 0 {
		return nil, errors.New("no such host")
	}
	answer := resolver.answers[0]
	if len(resolver.answers) > 1 {
		resolver.answers = resolver.answers[1:]
	}
	return answer, nil
}

func (resolver *recordingResolver) Calls() []string {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return slices.Clone(resolver.calls)
}

type redirectDialer struct {
	mu     sync.Mutex
	dialed []string
	target string
}

func (dialer *redirectDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer.mu.Lock()
	dialer.dialed = append(dialer.dialed, address)
	target := dialer.target
	dialer.mu.Unlock()
	var direct net.Dialer
	return direct.DialContext(ctx, network, target)
}

func (dialer *redirectDialer) Dialed() []string {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return slices.Clone(dialer.dialed)
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func echoServer(t *testing.T) string {
	t.Helper()
	listener := listen(t)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
			}()
		}
	}()
	return listener.Addr().String()
}

type gatewayHarness struct {
	t        *testing.T
	ingress  string
	egress   string
	resolver *recordingResolver
	dialer   *redirectDialer
	logs     *lockedBuffer
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(p []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(p)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func startGateway(t *testing.T, cfg Config, target string, answers [][]netip.Addr, limit int) *gatewayHarness {
	t.Helper()
	h := &gatewayHarness{
		t: t, resolver: &recordingResolver{answers: answers}, dialer: &redirectDialer{target: target}, logs: &lockedBuffer{},
	}
	if cfg.IngressListen == "" {
		cfg.IngressListen = "127.0.0.1:0"
	}
	if cfg.IngressTarget == "" {
		cfg.IngressTarget = "app:3000"
	}
	if cfg.EgressListen == "" {
		cfg.EgressListen = "127.0.0.1:0"
	}
	if len(cfg.EgressSources) == 0 {
		cfg.EgressSources = loopbackSources
	}
	g, err := newGateway(cfg, h.resolver, h.dialer, slog.New(slog.NewTextHandler(h.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if limit > 0 {
		g.limit = limit
	}
	ingress, egress := listen(t), listen(t)
	h.ingress, h.egress = ingress.Addr().String(), egress.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.serve(ctx, ingress, egress) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("gateway did not stop")
		}
	})
	return h
}

func public(addresses ...string) []netip.Addr {
	parsed := make([]netip.Addr, len(addresses))
	for index, address := range addresses {
		parsed[index] = netip.MustParseAddr(address)
	}
	return parsed
}

func (h *gatewayHarness) connect(authority string) (net.Conn, *bufio.Reader, int) {
	h.t.Helper()
	conn, err := net.Dial("tcp", h.egress)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n"); err != nil {
		h.t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		h.t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, reader, response.StatusCode
}

func assertEcho(t *testing.T, conn net.Conn, reader *bufio.Reader, message string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if _, err := io.WriteString(conn, message); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len(message))
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != message {
		t.Fatalf("echo = %q", buffer)
	}
}

func TestConnectTunnelsToAnAllowlistedHost(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	h := startGateway(t, Config{
		Allow:        []string{"API.Example.com."},
		AllowAddress: func(address netip.Addr) bool { return address == loopback },
	}, echoServer(t), [][]netip.Addr{{loopback}}, 0)
	conn, reader, status := h.connect("api.example.com.:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	assertEcho(t, conn, reader, "client hello")
	if dialed := h.dialer.Dialed(); !slices.Equal(dialed, []string{"127.0.0.1:443"}) {
		t.Fatalf("dialed = %v", dialed)
	}
	if calls := h.resolver.Calls(); !slices.Equal(calls, []string{"api.example.com"}) {
		t.Fatalf("lookups = %v", calls)
	}
}

func TestConnectRefusesAHostThatIsNotAllowlisted(t *testing.T) {
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, echoServer(t), [][]netip.Addr{public("93.184.216.34")}, 0)
	for _, authority := range []string{"evil.example.com:443", "sub.api.example.com:443", "example.com:443", "93.184.216.34:443"} {
		if _, _, status := h.connect(authority); status != http.StatusForbidden {
			t.Fatalf("%s status = %d", authority, status)
		}
	}
	if len(h.dialer.Dialed()) != 0 || len(h.resolver.Calls()) != 0 {
		t.Fatalf("refused host was resolved or dialled: %v %v", h.resolver.Calls(), h.dialer.Dialed())
	}
	if !strings.Contains(h.logs.String(), "host not allowed") || !strings.Contains(h.logs.String(), "evil.example.com") {
		t.Fatalf("logs = %s", h.logs.String())
	}
}

func TestConnectRefusesAllowlistedHostWithOnlyDeniedAddresses(t *testing.T) {
	for _, address := range []string{"10.0.0.1", "169.254.169.254", "127.0.0.1", "::ffff:10.0.0.1", "fd00::1"} {
		t.Run(address, func(t *testing.T) {
			h := startGateway(t, Config{Allow: []string{"api.example.com"}}, echoServer(t), [][]netip.Addr{public(address)}, 0)
			if _, _, status := h.connect("api.example.com:443"); status != http.StatusForbidden {
				t.Fatalf("status = %d", status)
			}
			if len(h.dialer.Dialed()) != 0 {
				t.Fatalf("dialled %v", h.dialer.Dialed())
			}
			if !strings.Contains(h.logs.String(), "no public address") {
				t.Fatalf("logs = %s", h.logs.String())
			}
		})
	}
}

func TestConnectRefusesPortsOtherThan443(t *testing.T) {
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, echoServer(t), [][]netip.Addr{public("93.184.216.34")}, 0)
	for _, authority := range []string{"api.example.com:80", "api.example.com:8443", "api.example.com:0443", "api.example.com"} {
		if _, _, status := h.connect(authority); status != http.StatusForbidden {
			t.Fatalf("%s status = %d", authority, status)
		}
	}
	if len(h.dialer.Dialed()) != 0 {
		t.Fatalf("dialled %v", h.dialer.Dialed())
	}
}

func TestConnectDialsOnlyTheCheckedAddress(t *testing.T) {
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, echoServer(t), [][]netip.Addr{
		public("10.0.0.9", "93.184.216.34"),
		public("10.0.0.1"),
	}, 0)
	conn, reader, status := h.connect("api.example.com:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	assertEcho(t, conn, reader, "first")
	if _, _, status := h.connect("api.example.com:443"); status != http.StatusForbidden {
		t.Fatalf("rebound lookup status = %d", status)
	}
	if dialed := h.dialer.Dialed(); !slices.Equal(dialed, []string{"93.184.216.34:443"}) {
		t.Fatalf("dialed = %v", dialed)
	}
	if calls := h.resolver.Calls(); len(calls) != 2 {
		t.Fatalf("lookups = %v", calls)
	}
}

func TestConnectTriesEachCheckedAddressInOrder(t *testing.T) {
	echo := echoServer(t)
	closed := listen(t)
	unreachable := closed.Addr().String()
	_ = closed.Close()
	failFirst := &orderedDialer{targets: []string{unreachable, echo}}
	g, err := newGateway(Config{
		IngressListen: "x:1", IngressTarget: "app:1", EgressListen: "x:2", Allow: []string{"api.example.com"},
		EgressSources: loopbackSources,
	},
		resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return public("93.184.216.34", "1.1.1.1"), nil
		}),
		failFirst, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := g.dialFirst(context.Background(), public("93.184.216.34", "1.1.1.1"), 443)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if !slices.Equal(failFirst.dialed, []string{"93.184.216.34:443", "1.1.1.1:443"}) {
		t.Fatalf("dialed = %v", failFirst.dialed)
	}
}

type orderedDialer struct {
	targets []string
	dialed  []string
}

func (dialer *orderedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	target := dialer.targets[len(dialer.dialed)]
	dialer.dialed = append(dialer.dialed, address)
	var direct net.Dialer
	return direct.DialContext(ctx, network, target)
}

func proxyRequest(t *testing.T, proxy, raw string) *http.Response {
	t.Helper()
	conn, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestPlainHTTPForwardStripsHopByHopHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen *http.Request
	upstream := listen(t)
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		mu.Lock()
		seen = request
		mu.Unlock()
		_, _ = io.WriteString(conn, "HTTP/1.1 418 I'm a teapot\r\nConnection: X-Upstream-Hop\r\nX-Upstream-Hop: 1\r\n"+
			"Proxy-Authenticate: Basic\r\nKeep-Alive: timeout=5\r\nX-Upstream: kept\r\nContent-Length: 13\r\n\r\nupstream body")
	}()
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, upstream.Addr().String(),
		[][]netip.Addr{public("93.184.216.34")}, 0)
	response := proxyRequest(t, h.egress, "GET http://api.example.com/path?q=1 HTTP/1.1\r\n"+
		"Host: api.example.com\r\nConnection: close, X-Client-Hop\r\nX-Client-Hop: 1\r\n"+
		"Proxy-Authorization: Basic c2VjcmV0\r\nProxy-Connection: keep-alive\r\nProxy-Custom: 1\r\n"+
		"Keep-Alive: timeout=5\r\nTe: trailers\r\nUpgrade: websocket\r\nX-Client: kept\r\n\r\n")
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusTeapot || string(body) != "upstream body" {
		t.Fatalf("response = %d %q", response.StatusCode, body)
	}
	if response.Header.Get("X-Upstream") != "kept" || response.Header.Get("X-Upstream-Hop") != "" ||
		response.Header.Get("Proxy-Authenticate") != "" || response.Header.Get("Keep-Alive") != "" {
		t.Fatalf("response headers = %v", response.Header)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen == nil {
		t.Fatal("upstream was not reached")
	}
	if seen.URL.RequestURI() != "/path?q=1" || seen.Host != "api.example.com" || seen.Header.Get("X-Client") != "kept" {
		t.Fatalf("upstream request = %s %s %v", seen.Host, seen.URL, seen.Header)
	}
	for _, name := range []string{"X-Client-Hop", "Proxy-Authorization", "Proxy-Connection", "Proxy-Custom", "Keep-Alive", "Te", "Upgrade"} {
		if value := seen.Header.Get(name); value != "" {
			t.Fatalf("upstream saw %s: %q", name, value)
		}
	}
	if dialed := h.dialer.Dialed(); !slices.Equal(dialed, []string{"93.184.216.34:80"}) {
		t.Fatalf("dialed = %v", dialed)
	}
}

type rawUpstream struct {
	addr     string
	mu       sync.Mutex
	requests []*http.Request
}

func startRawUpstream(t *testing.T, response string) *rawUpstream {
	t.Helper()
	listener := listen(t)
	t.Cleanup(func() { _ = listener.Close() })
	upstream := &rawUpstream{addr: listener.Addr().String()}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			request, err := http.ReadRequest(bufio.NewReader(conn))
			if err == nil {
				upstream.mu.Lock()
				upstream.requests = append(upstream.requests, request)
				upstream.mu.Unlock()
				_, _ = io.WriteString(conn, response)
			}
			_ = conn.Close()
		}
	}()
	return upstream
}

func (upstream *rawUpstream) Requests() []*http.Request {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	return slices.Clone(upstream.requests)
}

func TestPlainHTTPForwardAddsNoUserAgentOrCompression(t *testing.T) {
	upstream := startRawUpstream(t, "HTTP/1.1 204 No Content\r\n\r\n")
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, upstream.addr, [][]netip.Addr{public("93.184.216.34")}, 0)
	proxyRequest(t, h.egress, "GET http://api.example.com/a HTTP/1.1\r\nHost: api.example.com\r\n\r\n")
	proxyRequest(t, h.egress, "GET http://api.example.com/b HTTP/1.1\r\nHost: api.example.com\r\nUser-Agent: npm/10\r\nAccept-Encoding: br\r\n\r\n")
	requests := upstream.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	bare, explicit := requests[0], requests[1]
	if _, set := bare.Header["User-Agent"]; set {
		t.Fatalf("gateway added a User-Agent: %v", bare.Header)
	}
	if _, set := bare.Header["Accept-Encoding"]; set {
		t.Fatalf("gateway added an Accept-Encoding: %v", bare.Header)
	}
	if explicit.Header.Get("User-Agent") != "npm/10" || explicit.Header.Get("Accept-Encoding") != "br" {
		t.Fatalf("client headers were not forwarded: %v", explicit.Header)
	}
}

func TestForwardTransportUsesNoProxy(t *testing.T) {
	transport := forwardTransport(func(context.Context) (net.Conn, error) { return nil, errors.New("unused") })
	if transport.Proxy != nil {
		t.Fatal("forward transport consults a proxy")
	}
	if !transport.DisableKeepAlives || !transport.DisableCompression || transport.MaxResponseHeaderBytes != maxResponseHeaderBytes {
		t.Fatalf("transport = %+v", transport)
	}
}

func TestPlainHTTPForwardSendsOriginFormUpstream(t *testing.T) {
	upstream := startRawUpstream(t, "HTTP/1.1 204 No Content\r\n\r\n")
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, upstream.addr, [][]netip.Addr{public("93.184.216.34")}, 0)
	if response := proxyRequest(t, h.egress, "GET http://api.example.com/a?b=1 HTTP/1.1\r\nHost: api.example.com\r\n\r\n"); response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	requests := upstream.Requests()
	if len(requests) != 1 || requests[0].RequestURI != "/a?b=1" {
		t.Fatalf("upstream requests = %+v", requests)
	}
}

func TestPlainHTTPRefusals(t *testing.T) {
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, echoServer(t), [][]netip.Addr{public("93.184.216.34")}, 0)
	for raw, want := range map[string]int{
		"GET http://api.example.com:8080/ HTTP/1.1\r\nHost: api.example.com\r\n\r\n":                 http.StatusForbidden,
		"GET https://api.example.com/ HTTP/1.1\r\nHost: api.example.com\r\n\r\n":                     http.StatusForbidden,
		"GET http://evil.example.com/ HTTP/1.1\r\nHost: evil.example.com\r\n\r\n":                    http.StatusForbidden,
		"GET /relative HTTP/1.1\r\nHost: api.example.com\r\n\r\n":                                    http.StatusMethodNotAllowed,
		"GET http://api.example.com/ HTTP/1.1\r\nX-Big: " + strings.Repeat("a", 24<<10) + "\r\n\r\n": http.StatusRequestHeaderFieldsTooLarge,
	} {
		if response := proxyRequest(t, h.egress, raw); response.StatusCode != want {
			t.Fatalf("%.60q status = %d, want %d", raw, response.StatusCode, want)
		}
	}
	if len(h.dialer.Dialed()) != 0 {
		t.Fatalf("dialled %v", h.dialer.Dialed())
	}
}

func TestRefusalLogsAreRateBounded(t *testing.T) {
	h := startGateway(t, Config{Allow: []string{"api.example.com"}}, echoServer(t), nil, 0)
	for range 25 {
		if _, _, status := h.connect("evil.example.com:443"); status != http.StatusForbidden {
			t.Fatalf("status = %d", status)
		}
	}
	if lines := strings.Count(h.logs.String(), "preview egress refused"); lines < 1 || lines > refusalLogsPerSecond {
		t.Fatalf("refusal log lines = %d", lines)
	}
}

func TestIngressForwardsToTheTarget(t *testing.T) {
	h := startGateway(t, Config{IngressTarget: "app:3000"}, echoServer(t), nil, 0)
	conn, err := net.Dial("tcp", h.ingress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "GET / HTTP/1.0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(conn)
	if err != nil || string(body) != "GET / HTTP/1.0\r\n\r\n" {
		t.Fatalf("ingress echo = %q %v", body, err)
	}
	if dialed := h.dialer.Dialed(); !slices.Equal(dialed, []string{"app:3000"}) {
		t.Fatalf("dialed = %v", dialed)
	}
}

func TestConcurrencyCap(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	h := startGateway(t, Config{
		Allow: []string{"api.example.com"}, AllowAddress: func(address netip.Addr) bool { return address == loopback },
	}, echoServer(t), [][]netip.Addr{{loopback}}, 2)
	first, firstReader, status := h.connect("api.example.com:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	second, secondReader, status := h.connect("api.example.com:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	assertEcho(t, first, firstReader, "one")
	assertEcho(t, second, secondReader, "two")
	third, err := net.Dial("tcp", h.egress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.Close() }()
	if _, err := io.WriteString(third, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = third.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	reader := bufio.NewReader(third)
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("third tunnel was served beyond the cap")
	}
	_ = first.Close()
	_ = third.SetReadDeadline(time.Now().Add(5 * time.Second))
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("third tunnel after a slot freed = %v %v", response, err)
	}
	ingressFirst, err := net.Dial("tcp", h.ingress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ingressFirst.Close() }()
	ingressSecond, err := net.Dial("tcp", h.ingress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ingressSecond.Close() }()
	for _, conn := range []net.Conn{ingressFirst, ingressSecond} {
		assertEcho(t, conn, bufio.NewReader(conn), "in")
	}
	ingressThird, err := net.Dial("tcp", h.ingress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ingressThird.Close() }()
	_, _ = io.WriteString(ingressThird, "x")
	_ = ingressThird.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := ingressThird.Read(make([]byte, 1)); err == nil {
		t.Fatal("third ingress connection was served beyond the cap")
	}
	_ = ingressFirst.Close()
	_ = ingressThird.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(ingressThird, make([]byte, 1)); err != nil {
		t.Fatalf("third ingress connection after a slot freed: %v", err)
	}
}

func TestTunnelIdleTimeoutClosesAQuietTunnel(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	cfg := Config{
		IngressListen: "127.0.0.1:0", IngressTarget: "app:1", EgressListen: "127.0.0.1:0",
		Allow: []string{"api.example.com"}, AllowAddress: func(address netip.Addr) bool { return address == loopback },
		EgressSources: loopbackSources,
	}
	resolver := &recordingResolver{answers: [][]netip.Addr{{loopback}}}
	g, err := newGateway(cfg, resolver, &redirectDialer{target: echoServer(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.idle = 150 * time.Millisecond
	ingress, egress := listen(t), listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.serve(ctx, ingress, egress) }()
	defer func() {
		cancel()
		<-done
	}()
	h := &gatewayHarness{t: t, egress: egress.Addr().String()}
	conn, reader, status := h.connect("api.example.com:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	for range 3 {
		time.Sleep(80 * time.Millisecond)
		assertEcho(t, conn, reader, "keep")
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("quiet tunnel stayed open")
	}
}

func TestNewGatewayValidatesConfig(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil })
	dialer := &redirectDialer{}
	for name, cfg := range map[string]Config{
		"no ingress":           {IngressTarget: "app:1"},
		"no target":            {IngressListen: ":8080"},
		"target without port":  {IngressListen: ":8080", IngressTarget: "app"},
		"allow without egress": {IngressListen: ":8080", IngressTarget: "app:1", Allow: []string{"a.example.com"}},
		"empty allowed host":   {IngressListen: ":8080", IngressTarget: "app:1", EgressListen: ":3128", Allow: []string{"."}},
		"sources without egress": {
			IngressListen: ":8080", IngressTarget: "app:1", EgressSources: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		},
		"invalid source": {IngressListen: ":8080", IngressTarget: "app:1", EgressListen: ":3128", EgressSources: []netip.Prefix{{}}},
		"egress without sources": {
			IngressListen: ":8080", IngressTarget: "app:1", EgressListen: ":3128", Allow: []string{"a.example.com"},
		},
	} {
		if _, err := newGateway(cfg, resolver, dialer, nil); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := newGateway(Config{IngressListen: ":8080", IngressTarget: "app:1"}, nil, dialer, nil); err == nil {
		t.Fatal("missing resolver accepted")
	}
}

var loopbackSources = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}

type addressedConn struct {
	net.Conn
	local, remote net.Addr
	closed        bool
}

func (conn *addressedConn) LocalAddr() net.Addr { return conn.local }

func (conn *addressedConn) RemoteAddr() net.Addr { return conn.remote }

func (conn *addressedConn) Close() error {
	conn.closed = true
	return nil
}

func (conn *addressedConn) Read([]byte) (int, error) {
	panic("filtered connection was read")
}

type scriptedListener struct {
	conns []*addressedConn
}

func (listener *scriptedListener) Accept() (net.Conn, error) {
	if len(listener.conns) == 0 {
		return nil, net.ErrClosed
	}
	conn := listener.conns[0]
	listener.conns = listener.conns[1:]
	return conn, nil
}

func (listener *scriptedListener) Close() error { return nil }

func (listener *scriptedListener) Addr() net.Addr { return rawAddr("10.1.0.2:3128") }

func TestGatewayEgressRejectsConnectionToALocalAddressOutsideTheSources(t *testing.T) {
	sources := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}
	onJobNetwork := &addressedConn{local: rawAddr("10.2.0.3:3128"), remote: rawAddr("10.1.0.5:40000")}
	unparseableLocal := &addressedConn{local: rawAddr("garbage"), remote: rawAddr("10.1.0.5:40001")}
	missingLocal := &addressedConn{remote: rawAddr("10.1.0.5:40002")}
	foreignRemote := &addressedConn{local: rawAddr("10.1.0.2:3128"), remote: rawAddr("10.2.0.3:40000")}
	onPreviewNetwork := &addressedConn{local: rawAddr("[::ffff:10.1.0.2]:3128"), remote: rawAddr("10.1.0.5:40003")}
	filter := newSourceFilterListener(&scriptedListener{conns: []*addressedConn{onJobNetwork, unparseableLocal, missingLocal, foreignRemote, onPreviewNetwork}}, sources)
	conn, err := filter.Accept()
	if err != nil || conn != onPreviewNetwork {
		t.Fatalf("accepted = %v %v", conn, err)
	}
	for name, rejected := range map[string]*addressedConn{"job network": onJobNetwork, "unparseable": unparseableLocal, "missing": missingLocal, "foreign remote": foreignRemote} {
		if !rejected.closed {
			t.Fatalf("%s connection was not closed", name)
		}
	}
	if onPreviewNetwork.closed {
		t.Fatal("preview network connection was closed")
	}
	if _, err := filter.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept after script = %v", err)
	}
}

type rawAddr string

func (addr rawAddr) Network() string { return "tcp" }

func (addr rawAddr) String() string { return string(addr) }

func assertClosedWithoutResponse(t *testing.T, address string) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n")
	data, err := io.ReadAll(conn)
	if len(data) != 0 {
		t.Fatalf("foreign source got a response: %q", data)
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("foreign source connection was left open")
	}
}

func TestGatewayEgressRejectsForeignSource(t *testing.T) {
	foreign := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	local := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("127.0.0.0/8")}

	t.Run("filter", func(t *testing.T) {
		for name, test := range map[string]struct {
			sources []netip.Prefix
			accept  bool
		}{
			"foreign": {foreign, false},
			"local":   {local, true},
		} {
			t.Run(name, func(t *testing.T) {
				raw := listen(t)
				filter := newSourceFilterListener(raw, test.sources)
				t.Cleanup(func() { _ = filter.Close() })
				accepted := make(chan net.Conn, 1)
				go func() {
					conn, err := filter.Accept()
					if err == nil {
						accepted <- conn
					}
				}()
				if test.accept {
					conn, err := net.Dial("tcp", raw.Addr().String())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = conn.Close() }()
					select {
					case server := <-accepted:
						_ = server.Close()
					case <-time.After(5 * time.Second):
						t.Fatal("local source was not accepted")
					}
					return
				}
				assertClosedWithoutResponse(t, raw.Addr().String())
				select {
				case <-accepted:
					t.Fatal("foreign source reached Accept")
				case <-time.After(50 * time.Millisecond):
				}
			})
		}
	})

	t.Run("unparseable remote", func(t *testing.T) {
		filter := newSourceFilterListener(nil, local)
		for _, remote := range []net.Addr{nil, rawAddr("garbage"), rawAddr("127.0.0.1"), rawAddr("[::ffff:10.1.2.3]:1")} {
			want := remote != nil && remote.String() == "[::ffff:10.1.2.3]:1"
			if got := filter.permits(remote); got != want {
				t.Fatalf("permits(%v) = %v", remote, got)
			}
		}
	})

	t.Run("gateway", func(t *testing.T) {
		loopback := netip.MustParseAddr("127.0.0.1")
		cfg := Config{
			Allow:        []string{"api.example.com"},
			AllowAddress: func(address netip.Addr) bool { return address == loopback },
		}
		cfg.EgressSources = foreign
		h := startGateway(t, cfg, echoServer(t), [][]netip.Addr{{loopback}}, 0)
		assertClosedWithoutResponse(t, h.egress)
		if len(h.resolver.Calls()) != 0 || len(h.dialer.Dialed()) != 0 {
			t.Fatalf("foreign source was served: %v %v", h.resolver.Calls(), h.dialer.Dialed())
		}
		ingress, err := net.Dial("tcp", h.ingress)
		if err != nil {
			t.Fatal(err)
		}
		_ = ingress.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(ingress, "ping")
		_ = ingress.(*net.TCPConn).CloseWrite()
		body, err := io.ReadAll(ingress)
		_ = ingress.Close()
		if err != nil || string(body) != "ping" {
			t.Fatalf("ingress echo = %q %v", body, err)
		}

		cfg.EgressSources = local
		allowed := startGateway(t, cfg, echoServer(t), [][]netip.Addr{{loopback}}, 0)
		conn, reader, status := allowed.connect("api.example.com:443")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		assertEcho(t, conn, reader, "client hello")
	})
}
