package remoteconfig

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/identity"
)

type testAuthority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
}

func newTestAuthority(t *testing.T, name string) testAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-48 * time.Hour),
		NotAfter:              time.Now().Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testAuthority{certificate: certificate, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (a testAuthority) issue(t *testing.T, template *x509.Certificate) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func (a testAuthority) clientStateDir(t *testing.T, notBefore, notAfter time.Time, gatewayCA []byte) string {
	t.Helper()
	key, certificate := a.issue(t, &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: testRunnerID},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := identity.NewStore(stateDir).SaveIdentity(key, certificate, gatewayCA); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

func startRefusingGateway(t *testing.T, authority testAuthority) string {
	t.Helper()
	key, certificate := authority.issue(t, &x509.Certificate{
		SerialNumber: big.NewInt(2),
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
	body := encode(t, deliveryBody(dockerConfig()))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clients,
		MinVersion:   tls.VersionTLS13,
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return "wss://" + server.Listener.Addr().String() + "/v1/runners/stream"
}

func TestFetcherTreatsATLSRefusalOfTheRunnerCertificateAsAnIdentityRefusal(t *testing.T) {
	authority := newTestAuthority(t, "runner CA")
	gateway := startRefusingGateway(t, authority)
	valid := authority.clientStateDir(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), authority.pem)
	fetcher, err := NewFetcher(valid, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.Fetch(context.Background()); err != nil {
		t.Fatalf("valid identity refused: %v", err)
	}
	other := newTestAuthority(t, "replaced runner CA")
	refused := map[string]string{
		"expired":    authority.clientStateDir(t, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour), authority.pem),
		"unknown CA": other.clientStateDir(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), authority.pem),
	}
	for name, stateDir := range refused {
		fetcher, err := NewFetcher(stateDir, gateway)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fetcher.Fetch(context.Background())
		var refusal *IdentityRefusedError
		if !errors.As(err, &refusal) || IsTransient(err) {
			t.Fatalf("%s: err = %v, transient = %v", name, err, IsTransient(err))
		}
		if !strings.Contains(err.Error(), "did not accept this runner's identity") {
			t.Fatalf("%s: message = %v", name, err)
		}

		cached := encode(t, deliveryBody(dockerConfig()))
		local := configuredState(t, cached)
		result, err := Resolve(context.Background(), local.StateDir, testRunnerID, func(string) (FetchFunc, error) {
			return fetcher.Fetch, nil
		}, noSleepPolicy())
		if !errors.As(err, &refusal) || result.FromCache {
			t.Fatalf("%s: resolve err = %v, from cache = %v", name, err, result.FromCache)
		}
	}
}

func TestResolveStillUsesTheCacheWhenTheGatewayIsUnreachable(t *testing.T) {
	authority := newTestAuthority(t, "runner CA")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	stateDir := authority.clientStateDir(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), authority.pem)
	fetcher, err := NewFetcher(stateDir, "wss://"+address+"/v1/runners/stream")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background())
	var refusal *IdentityRefusedError
	if errors.As(err, &refusal) || !IsTransient(err) {
		t.Fatalf("connect refused err = %v", err)
	}
	local := configuredState(t, encode(t, deliveryBody(dockerConfig())))
	result, err := Resolve(context.Background(), local.StateDir, testRunnerID, func(string) (FetchFunc, error) {
		return fetcher.Fetch, nil
	}, noSleepPolicy())
	if err != nil || !result.FromCache {
		t.Fatalf("resolve err = %v, from cache = %v", err, result.FromCache)
	}
}
