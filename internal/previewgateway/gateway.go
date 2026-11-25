package previewgateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/example/app-runner/internal/netclass"
)

const (
	maxConnections       = 256
	headerReadTimeout    = 10 * time.Second
	tunnelIdleTimeout    = 5 * time.Minute
	maxHeaderBytes       = 16 << 10
	lookupTimeout        = 10 * time.Second
	dialTimeout          = 10 * time.Second
	refusalLogsPerSecond = 10
	maxLoggedHostLength  = 255

	maxResponseHeaderBytes = 1 << 20

	egressAddressPoll = 50 * time.Millisecond
	egressAddressWait = 2 * time.Minute
)

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type Config struct {
	IngressListen string
	IngressTarget string
	EgressListen  string
	Allow         []string
	AllowAddress  func(netip.Addr) bool
	EgressSources []netip.Prefix
}

type gateway struct {
	cfg          Config
	resolver     Resolver
	dialer       Dialer
	logger       *slog.Logger
	allow        map[string]bool
	allowAddress func(netip.Addr) bool
	limit        int
	idle         time.Duration
	refusals     refusalLimiter
	tracker      connTracker
}

func Run(ctx context.Context, cfg Config, resolver Resolver, dialer Dialer, logger *slog.Logger) error {
	g, err := newGateway(cfg, resolver, dialer, logger)
	if err != nil {
		return err
	}
	var egress net.Listener
	if cfg.EgressListen != "" {
		if egress, err = listenEgress(ctx, cfg.EgressListen, cfg.EgressSources, net.InterfaceAddrs); err != nil {
			return err
		}
	}
	ingress, err := net.Listen("tcp", cfg.IngressListen)
	if err != nil {
		if egress != nil {
			_ = egress.Close()
		}
		return err
	}
	return g.serve(ctx, ingress, egress)
}

func listenEgress(ctx context.Context, address string, sources []netip.Prefix, local func() ([]net.Addr, error)) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(host); host != "" && (err != nil || !ip.IsUnspecified()) {
		return net.Listen("tcp", address)
	}
	ctx, cancel := context.WithTimeout(ctx, egressAddressWait)
	defer cancel()
	for {
		ip, err := sourceAddress(sources, local)
		if err != nil {
			return nil, fmt.Errorf("list preview gateway addresses: %w", err)
		}
		if ip.IsValid() {
			return net.Listen("tcp", net.JoinHostPort(ip.String(), port))
		}
		timer := time.NewTimer(egressAddressPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.New("preview gateway has no local address inside the egress sources")
		case <-timer.C:
		}
	}
}

func sourceAddress(sources []netip.Prefix, local func() ([]net.Addr, error)) (netip.Addr, error) {
	addresses, err := local()
	if err != nil {
		return netip.Addr{}, err
	}
	var found []netip.Addr
	for _, address := range addresses {
		ip, ok := interfaceIP(address)
		if ok && slices.ContainsFunc(sources, func(source netip.Prefix) bool { return source.Contains(ip) }) {
			found = append(found, ip)
		}
	}
	if len(found) == 0 {
		return netip.Addr{}, nil
	}
	return slices.MinFunc(found, netip.Addr.Compare), nil
}

func interfaceIP(address net.Addr) (netip.Addr, bool) {
	var ip net.IP
	switch typed := address.(type) {
	case *net.IPNet:
		ip = typed.IP
	case *net.IPAddr:
		ip = typed.IP
	default:
		return netip.Addr{}, false
	}
	parsed, ok := netip.AddrFromSlice(ip)
	return parsed.Unmap(), ok
}

func newGateway(cfg Config, resolver Resolver, dialer Dialer, logger *slog.Logger) (*gateway, error) {
	if cfg.IngressListen == "" || cfg.IngressTarget == "" {
		return nil, errors.New("preview gateway needs an ingress listen address and target")
	}
	if _, _, err := net.SplitHostPort(cfg.IngressTarget); err != nil {
		return nil, errors.New("preview gateway ingress target must be host:port")
	}
	if cfg.EgressListen == "" && len(cfg.Allow) > 0 {
		return nil, errors.New("preview gateway allow list needs an egress listen address")
	}
	if cfg.EgressListen == "" && len(cfg.EgressSources) > 0 {
		return nil, errors.New("preview gateway egress sources need an egress listen address")
	}
	if cfg.EgressListen != "" && len(cfg.EgressSources) == 0 {
		return nil, errors.New("preview gateway egress listen address needs at least one egress source")
	}
	for _, source := range cfg.EgressSources {
		if !source.IsValid() {
			return nil, errors.New("preview gateway egress source is not a valid prefix")
		}
	}
	if resolver == nil || dialer == nil {
		return nil, errors.New("preview gateway needs a resolver and a dialer")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	allow := make(map[string]bool, len(cfg.Allow))
	for _, host := range cfg.Allow {
		normalized := normalizeHost(host)
		if normalized == "" {
			return nil, errors.New("preview gateway allow list has an empty host")
		}
		allow[normalized] = true
	}
	allowAddress := cfg.AllowAddress
	if allowAddress == nil {
		allowAddress = netclass.Public
	}
	return &gateway{
		cfg: cfg, resolver: resolver, dialer: dialer, logger: logger, allow: allow,
		allowAddress: allowAddress, limit: maxConnections, idle: tunnelIdleTimeout,
		tracker: connTracker{conns: map[net.Conn]struct{}{}},
	}, nil
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func (g *gateway) serve(ctx context.Context, ingress, egress net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	ingressLimited := newLimitListener(ingress, g.limit)
	go func() { errs <- g.serveIngress(ctx, ingressLimited) }()
	var server *http.Server
	if egress != nil {
		server = &http.Server{
			Handler:           http.HandlerFunc(g.proxy),
			ReadHeaderTimeout: headerReadTimeout,
			IdleTimeout:       tunnelIdleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
		if len(g.cfg.EgressSources) > 0 {
			egress = newSourceFilterListener(egress, g.cfg.EgressSources)
		}
		egressLimited := newLimitListener(egress, g.limit)
		go func() { errs <- server.Serve(egressLimited) }()
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	cancel()
	_ = ingressLimited.Close()
	if server != nil {
		_ = server.Close()
	}
	g.tracker.closeAll()
	if ctx.Err() != nil && (errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed)) {
		err = nil
	}
	return err
}

func (g *gateway) serveIngress(ctx context.Context, listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go g.forwardIngress(ctx, conn)
	}
}

func (g *gateway) forwardIngress(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if !g.tracker.add(conn) {
		return
	}
	defer g.tracker.remove(conn)
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	upstream, err := g.dialer.DialContext(dialCtx, "tcp", g.cfg.IngressTarget)
	cancel()
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()
	if !g.tracker.add(upstream) {
		return
	}
	defer g.tracker.remove(upstream)
	pipe(conn, upstream, 0)
}

func (g *gateway) proxy(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodConnect {
		g.connect(writer, request)
		return
	}
	if !request.URL.IsAbs() {
		g.refuse(writer, http.StatusMethodNotAllowed, request.Host, "not a proxy request")
		return
	}
	g.forward(writer, request)
}

func (g *gateway) connect(writer http.ResponseWriter, request *http.Request) {
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || port != "443" {
		g.refuse(writer, http.StatusForbidden, request.Host, "port not allowed")
		return
	}
	addresses, ok := g.checkedAddresses(writer, request, host)
	if !ok {
		return
	}
	upstream, err := g.dialFirst(request.Context(), addresses, 443)
	if err != nil {
		g.refuse(writer, http.StatusBadGateway, host, "connect failed")
		return
	}
	defer func() { _ = upstream.Close() }()
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		g.refuse(writer, http.StatusInternalServerError, host, "tunnel unavailable")
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	if !g.tracker.add(client) {
		return
	}
	defer g.tracker.remove(client)
	if !g.tracker.add(upstream) {
		return
	}
	defer g.tracker.remove(upstream)
	_ = client.SetDeadline(time.Time{})
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	if pending := buffered.Reader.Buffered(); pending > 0 {
		data, _ := buffered.Reader.Peek(pending)
		if _, err := upstream.Write(data); err != nil {
			return
		}
	}
	pipe(client, upstream, g.idle)
}

func (g *gateway) forward(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Scheme != "http" {
		g.refuse(writer, http.StatusForbidden, request.URL.Host, "scheme not allowed")
		return
	}
	if port := request.URL.Port(); port != "" && port != "80" {
		g.refuse(writer, http.StatusForbidden, request.URL.Host, "port not allowed")
		return
	}
	addresses, ok := g.checkedAddresses(writer, request, request.URL.Hostname())
	if !ok {
		return
	}
	transport := forwardTransport(func(ctx context.Context) (net.Conn, error) {
		return g.dialFirst(ctx, addresses, 80)
	})
	defer transport.CloseIdleConnections()
	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	outbound.Header = withoutHopHeaders(request.Header)
	if _, set := outbound.Header["User-Agent"]; !set {
		outbound.Header.Set("User-Agent", "")
	}
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		g.refuse(writer, http.StatusBadGateway, request.URL.Hostname(), "forward failed")
		return
	}
	defer func() { _ = response.Body.Close() }()
	for key, values := range withoutHopHeaders(response.Header) {
		writer.Header()[key] = values
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func (g *gateway) checkedAddresses(writer http.ResponseWriter, request *http.Request, host string) ([]netip.Addr, bool) {
	host = normalizeHost(host)
	if !g.allow[host] {
		g.refuse(writer, http.StatusForbidden, host, "host not allowed")
		return nil, false
	}
	lookupCtx, cancel := context.WithTimeout(request.Context(), lookupTimeout)
	defer cancel()
	resolved, err := g.resolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		g.refuse(writer, http.StatusBadGateway, host, "lookup failed")
		return nil, false
	}
	var kept []netip.Addr
	for _, address := range resolved {
		address = address.Unmap()
		if g.allowAddress(address) && !containsAddress(kept, address) {
			kept = append(kept, address)
		}
	}
	if len(kept) == 0 {
		g.refuse(writer, http.StatusForbidden, host, "no public address")
		return nil, false
	}
	return kept, true
}

func containsAddress(addresses []netip.Addr, address netip.Addr) bool {
	for _, existing := range addresses {
		if existing == address {
			return true
		}
	}
	return false
}

func (g *gateway) dialFirst(ctx context.Context, addresses []netip.Addr, port uint16) (net.Conn, error) {
	err := errors.New("no address to dial")
	for _, address := range addresses {
		dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, dialErr := g.dialer.DialContext(dialCtx, "tcp", netip.AddrPortFrom(address, port).String())
		cancel()
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
		if ctx.Err() != nil {
			break
		}
	}
	return nil, err
}

func (g *gateway) refuse(writer http.ResponseWriter, status int, host, reason string) {
	if g.refusals.allow(time.Now()) {
		if len(host) > maxLoggedHostLength {
			host = host[:maxLoggedHostLength]
		}
		g.logger.Warn("preview egress refused", "host", host, "reason", reason)
	}
	http.Error(writer, reason, status)
}

var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func forwardTransport(dial func(ctx context.Context) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		Proxy:              nil,
		DisableKeepAlives:  true,
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx)
		},
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
	}
}

func withoutHopHeaders(header http.Header) http.Header {
	cleaned := header.Clone()
	if cleaned == nil {
		cleaned = http.Header{}
	}
	for _, value := range header.Values("Connection") {
		for _, field := range strings.Split(value, ",") {
			if field = strings.TrimSpace(field); field != "" {
				cleaned.Del(field)
			}
		}
	}
	for _, name := range hopHeaders {
		cleaned.Del(name)
	}
	for key := range cleaned {
		if strings.HasPrefix(http.CanonicalHeaderKey(key), "Proxy-") {
			delete(cleaned, key)
		}
	}
	return cleaned
}

func pipe(client, upstream net.Conn, idle time.Duration) {
	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	closeBoth := func() {
		_ = client.Close()
		_ = upstream.Close()
	}
	done := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		buffer := make([]byte, 32<<10)
		for {
			if idle > 0 {
				_ = src.SetReadDeadline(time.Now().Add(idle))
			}
			n, err := src.Read(buffer)
			if n > 0 {
				lastActive.Store(time.Now().UnixNano())
				if idle > 0 {
					_ = dst.SetWriteDeadline(time.Now().Add(idle))
				}
				if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
					closeBoth()
					return
				}
			}
			if err == nil {
				continue
			}
			var netErr net.Error
			if idle > 0 && errors.As(err, &netErr) && netErr.Timeout() &&
				time.Since(time.Unix(0, lastActive.Load())) < idle {
				continue
			}
			if errors.Is(err, io.EOF) {
				closeWrite(dst)
				return
			}
			closeBoth()
			return
		}
	}
	go copyHalf(upstream, client)
	go copyHalf(client, upstream)
	<-done
	<-done
}

func closeWrite(conn net.Conn) {
	if writer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = writer.CloseWrite()
		return
	}
	_ = conn.Close()
}

type refusalLimiter struct {
	mu     sync.Mutex
	window time.Time
	count  int
}

func (limiter *refusalLimiter) allow(now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if now.Sub(limiter.window) >= time.Second {
		limiter.window = now
		limiter.count = 0
	}
	if limiter.count >= refusalLogsPerSecond {
		return false
	}
	limiter.count++
	return true
}

type connTracker struct {
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

func (tracker *connTracker) add(conn net.Conn) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.closed {
		_ = conn.Close()
		return false
	}
	tracker.conns[conn] = struct{}{}
	return true
}

func (tracker *connTracker) remove(conn net.Conn) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	delete(tracker.conns, conn)
}

func (tracker *connTracker) closeAll() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.closed = true
	for conn := range tracker.conns {
		_ = conn.Close()
	}
	clear(tracker.conns)
}

type limitListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newLimitListener(listener net.Listener, limit int) *limitListener {
	return &limitListener{Listener: listener, slots: make(chan struct{}, limit), done: make(chan struct{})}
}

func (listener *limitListener) Accept() (net.Conn, error) {
	select {
	case listener.slots <- struct{}{}:
	case <-listener.done:
		return nil, net.ErrClosed
	}
	conn, err := listener.Listener.Accept()
	if err != nil {
		<-listener.slots
		return nil, err
	}
	return &limitedConn{Conn: conn, release: func() { <-listener.slots }}, nil
}

func (listener *limitListener) Close() error {
	listener.once.Do(func() { close(listener.done) })
	return listener.Listener.Close()
}

type sourceFilterListener struct {
	net.Listener
	sources []netip.Prefix
}

func newSourceFilterListener(listener net.Listener, sources []netip.Prefix) *sourceFilterListener {
	return &sourceFilterListener{Listener: listener, sources: slices.Clone(sources)}
}

func (listener *sourceFilterListener) Accept() (net.Conn, error) {
	for {
		conn, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if listener.permits(conn.RemoteAddr()) && listener.permits(conn.LocalAddr()) {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func (listener *sourceFilterListener) permits(endpoint net.Addr) bool {
	if endpoint == nil {
		return false
	}
	address, err := netip.ParseAddrPort(endpoint.String())
	if err != nil {
		return false
	}
	ip := address.Addr().Unmap()
	for _, prefix := range listener.sources {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (conn *limitedConn) Close() error {
	err := conn.Conn.Close()
	conn.once.Do(conn.release)
	return err
}

func (conn *limitedConn) CloseWrite() error {
	if writer, ok := conn.Conn.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return conn.Close()
}
