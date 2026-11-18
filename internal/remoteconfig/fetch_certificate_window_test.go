package remoteconfig

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func startGatewayClosingWithoutAlert(t *testing.T, authority testAuthority, closeEveryConnection bool) string {
	t.Helper()
	key, certificate := authority.issue(t, &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "gateway"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certificate, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	clients := x509.NewCertPool()
	clients.AddCert(authority.certificate)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	body := encode(t, deliveryBody(dockerConfig()))
	config := &tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	}
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func(raw net.Conn) {
				defer func() { _ = raw.Close() }()
				conn := tls.Server(raw, config)
				if err := conn.Handshake(); err != nil {
					return
				}
				closeWithoutAlert := func() {
					_ = raw.(*net.TCPConn).CloseWrite()
					_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
					_, _ = io.Copy(io.Discard, raw)
				}
				peers := conn.ConnectionState().PeerCertificates
				if closeEveryConnection {
					_, _ = http.ReadRequest(bufio.NewReader(conn))
					closeWithoutAlert()
					return
				}
				if len(peers) == 0 {
					closeWithoutAlert()
					return
				}
				if _, err := peers[0].Verify(x509.VerifyOptions{Roots: clients, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
					closeWithoutAlert()
					return
				}
				if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
					return
				}
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}(raw)
		}
	}()
	return "wss://" + listener.Addr().String() + "/v1/runners/stream"
}

func TestFetcherRefusesARunnerCertificateOutsideItsValidityWindowBeforeConnecting(t *testing.T) {
	authority := newTestAuthority(t, "runner CA")
	gateway := startGatewayClosingWithoutAlert(t, authority, false)
	valid := authority.clientStateDir(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), authority.pem)
	fetcher, err := NewFetcher(valid, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.Fetch(context.Background()); err != nil {
		t.Fatalf("valid identity refused: %v", err)
	}
	outside := map[string]struct {
		stateDir string
		reason   string
	}{
		"expired":              {authority.clientStateDir(t, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour), authority.pem), "expired"},
		"not yet valid":        {authority.clientStateDir(t, time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour), authority.pem), "not valid until"},
		"expired a second ago": {authority.clientStateDir(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Second), authority.pem), "expired"},
	}
	for name, identity := range outside {
		fetcher, err := NewFetcher(identity.stateDir, gateway)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fetcher.Fetch(context.Background())
		var refusal *IdentityRefusedError
		if !errors.As(err, &refusal) || IsTransient(err) {
			t.Fatalf("%s: err = %v, transient = %v", name, err, IsTransient(err))
		}
		if !strings.Contains(err.Error(), "did not accept this runner's identity") || !strings.Contains(err.Error(), identity.reason) {
			t.Fatalf("%s: message = %v", name, err)
		}

		local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
		result, err := Resolve(context.Background(), local.StateDir, testRunnerID, func(string) (FetchFunc, error) {
			return fetcher.Fetch, nil
		}, noSleepPolicy())
		if !errors.As(err, &refusal) || result.FromCache {
			t.Fatalf("%s: resolve err = %v, from cache = %v", name, err, result.FromCache)
		}
	}
}

func TestResolveKeepsUsingTheCacheWhenTheGatewayClosesAfterTheHandshakeForAValidCertificate(t *testing.T) {
	authority := newTestAuthority(t, "runner CA")
	gateway := startGatewayClosingWithoutAlert(t, authority, true)
	stateDir := authority.clientStateDir(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), authority.pem)
	fetcher, err := NewFetcher(stateDir, gateway)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background())
	var refusal *IdentityRefusedError
	if errors.As(err, &refusal) || !IsTransient(err) || !errors.Is(err, io.EOF) {
		t.Fatalf("closed after handshake err = %v", err)
	}
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	result, err := Resolve(context.Background(), local.StateDir, testRunnerID, func(string) (FetchFunc, error) {
		return fetcher.Fetch, nil
	}, noSleepPolicy())
	if err != nil || !result.FromCache {
		t.Fatalf("resolve err = %v, from cache = %v", err, result.FromCache)
	}
}
