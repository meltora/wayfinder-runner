package previewgateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	previewNetworkAddress = netip.MustParseAddr("127.0.0.1")
	jobNetworkAddress     = netip.MustParseAddr("127.0.0.2")
	previewNetworkSources = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
)

func twoNetworkAddrs() ([]net.Addr, error) {
	return []net.Addr{
		&net.IPNet{IP: net.IPv4(127, 0, 0, 2), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)},
	}, nil
}

func requireSecondLoopback(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("needs the whole 127.0.0.0/8 range on the loopback interface")
	}
}

func egressPort(t *testing.T, listener net.Listener) uint16 {
	t.Helper()
	address, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return address.Port()
}

func TestEgressListenerBindsOnlyTheAddressInsideTheSources(t *testing.T) {
	requireSecondLoopback(t)
	listener, err := listenEgress(context.Background(), ":0", previewNetworkSources, twoNetworkAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := egressPort(t, listener)
	conn, err := net.DialTimeout("tcp", netip.AddrPortFrom(jobNetworkAddress, port).String(), 2*time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("egress listener %s accepts on the job network address", listener.Addr())
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial from the job network = %v, want connection refused", err)
	}
	conn, err = net.DialTimeout("tcp", netip.AddrPortFrom(previewNetworkAddress, port).String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial on the preview network: %v", err)
	}
	_ = conn.Close()
}

func TestEgressListenerWaitsForTheSourceAddress(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	local := func() ([]net.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return []net.Addr{&net.IPNet{IP: net.IPv4(10, 9, 0, 2), Mask: net.CIDRMask(16, 32)}}, nil
		}
		return []net.Addr{&net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}}, nil
	}
	listener, err := listenEgress(context.Background(), "0.0.0.0:0", previewNetworkSources, local)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if got := listener.Addr().String(); got != netip.AddrPortFrom(previewNetworkAddress, egressPort(t, listener)).String() {
		t.Fatalf("egress listener bound %s", got)
	}
}

func TestEgressListenerGivesUpWhenNoSourceAddressAppears(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	local := func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.IPv4(10, 9, 0, 2), Mask: net.CIDRMask(16, 32)}}, nil
	}
	if _, err := listenEgress(ctx, ":0", previewNetworkSources, local); err == nil {
		t.Fatal("egress listener started without a source address")
	}
	failing := func() ([]net.Addr, error) { return nil, errors.New("no interfaces") }
	if _, err := listenEgress(context.Background(), ":0", previewNetworkSources, failing); err == nil {
		t.Fatal("egress listener ignored an interface listing error")
	}
}

func TestEgressListenerKeepsAnExplicitAddress(t *testing.T) {
	listener, err := listenEgress(context.Background(), "127.0.0.1:0", []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, twoNetworkAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	address, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil || address.Addr() != previewNetworkAddress {
		t.Fatalf("egress listener bound %s %v", listener.Addr(), err)
	}
}

type throttledListener struct {
	net.Listener
	delay time.Duration
}

func (listener *throttledListener) Accept() (net.Conn, error) {
	time.Sleep(listener.delay)
	return listener.Listener.Accept()
}

func TestJobNetworkFloodDoesNotStarvePreviewEgress(t *testing.T) {
	requireSecondLoopback(t)
	cfg := Config{
		IngressListen: "127.0.0.1:0", IngressTarget: "app:1", EgressListen: ":0",
		Allow:         []string{"api.example.com"},
		AllowAddress:  func(address netip.Addr) bool { return address == previewNetworkAddress },
		EgressSources: previewNetworkSources,
	}
	resolver := &recordingResolver{answers: [][]netip.Addr{{previewNetworkAddress}}}
	g, err := newGateway(cfg, resolver, &redirectDialer{target: echoServer(t)}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	egress, err := listenEgress(context.Background(), cfg.EgressListen, cfg.EgressSources, twoNetworkAddrs)
	if err != nil {
		t.Fatal(err)
	}
	port := egressPort(t, egress)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	throttled := &throttledListener{Listener: egress, delay: 2 * time.Millisecond}
	go func() { done <- g.serve(ctx, listen(t), throttled) }()
	defer func() {
		cancel()
		<-done
	}()

	flood, stopFlood := context.WithCancel(context.Background())
	var flooders sync.WaitGroup
	target := netip.AddrPortFrom(jobNetworkAddress, port).String()
	for range 64 {
		flooders.Add(1)
		go func() {
			defer flooders.Done()
			dialer := net.Dialer{Timeout: time.Second}
			for flood.Err() == nil {
				conn, err := dialer.DialContext(flood, "tcp", target)
				if err != nil {
					continue
				}
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetLinger(0)
				}
				_ = conn.Close()
			}
		}()
	}
	defer func() {
		stopFlood()
		flooders.Wait()
	}()
	time.Sleep(300 * time.Millisecond)

	allowed := netip.AddrPortFrom(previewNetworkAddress, port).String()
	for attempt := range 5 {
		started := time.Now()
		conn, err := net.DialTimeout("tcp", allowed, 2*time.Second)
		if err != nil {
			t.Fatalf("attempt %d: preview egress dial during the flood: %v", attempt, err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(conn, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n")
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
		_ = conn.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: preview egress during the flood = %v %v after %s", attempt, response, err, time.Since(started))
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("attempt %d: preview egress took %s during the flood", attempt, elapsed)
		}
	}
}
