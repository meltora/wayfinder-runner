package previewobserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func previewTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "preview test root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "branch.preview.example.com"},
		DNSNames:  []string{"branch.preview.example.com"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
	certificate, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func observerRegistry(t *testing.T, handler http.Handler) (*Registry, *atomic.Int64) {
	t.Helper()
	server, caPEM := previewTLSServer(t, handler)
	dir := t.TempDir()
	caPath := filepath.Join(dir, "preview-ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	authorizationPath := filepath.Join(dir, "authorization")
	if err := os.WriteFile(authorizationPath, []byte("Bearer runner-local-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := observerProfileConfig()
	profile.TrustedCAFile = caPath
	profile.AuthorizationFile = authorizationPath
	profile.MaxResponseBytes = 1024
	var calls atomic.Int64
	registry, err := NewRegistry(
		map[string]config.PreviewObservationProfileConfig{"node-preview": profile},
		Dependencies{
			LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if network != "tcp" || address != "93.184.216.34:443" {
					t.Fatalf("unpinned dial = %s %s", network, address)
				}
				calls.Add(1)
				return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return registry, &calls
}

func completeObservationOperation() v1.PreviewObservationOperation {
	operation := targetOperation("https://branch.preview.example.com/review")
	operation.CommitSet = []v1.PreviewObservationCommit{
		{RepositoryID: "api", CommitSHA: strings.Repeat("a", 40)},
		{RepositoryID: "web", CommitSHA: strings.Repeat("b", 40)},
	}
	operation.LeaseExpiresAt = time.Now().Add(time.Minute)
	operation.PreviewExpiresAt = time.Now().Add(time.Hour)
	return operation
}

func TestObserveRunsExactChecksAgainstOnePinnedTLSAuthority(t *testing.T) {
	var requests atomic.Int64
	registry, dials := observerRegistry(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Host != "branch.preview.example.com" || request.Header.Get("Authorization") != "Bearer runner-local-secret" {
			t.Errorf("request authority/header = %q / %q", request.Host, request.Header.Get("Authorization"))
		}
		if request.URL.Path == "/api/health" {
			writer.WriteHeader(http.StatusOK)
			return
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	outcome := registry.Observe(context.Background(), completeObservationOperation())
	if outcome.Kind != v1.PreviewObservationObserved || outcome.Observation == nil {
		t.Fatalf("outcome = %#v", outcome)
	}
	if requests.Load() != 2 || dials.Load() < 1 || len(outcome.Observation.Checks) != 2 {
		t.Fatalf("requests=%d dials=%d observation=%#v", requests.Load(), dials.Load(), outcome.Observation)
	}
	if outcome.Observation.Checks[0].CheckID != "api" || outcome.Observation.Checks[0].Status != v1.PreviewObservationCheckPassed ||
		outcome.Observation.Checks[1].CheckID != "root" || outcome.Observation.Checks[1].Status != v1.PreviewObservationCheckFailed ||
		outcome.Observation.Checks[1].HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("checks = %#v", outcome.Observation.Checks)
	}
}

func TestObserveDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	registry, _ := observerRegistry(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirected" {
			redirected.Store(true)
		}
		http.Redirect(writer, request, "https://evil.example/redirected", http.StatusFound)
	}))
	outcome := registry.Observe(context.Background(), completeObservationOperation())
	if outcome.Kind != v1.PreviewObservationObserved || outcome.Observation.Checks[0].HTTPStatus != http.StatusFound {
		t.Fatalf("redirect outcome = %#v", outcome)
	}
	if redirected.Load() {
		t.Fatal("observer followed a redirect")
	}
}

func TestObserveRejectsOversizedBodiesWithoutReturningDiagnostics(t *testing.T) {
	registry, _ := observerRegistry(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("secret-response", 100)))
	}))
	outcome := registry.Observe(context.Background(), completeObservationOperation())
	if outcome.Kind != v1.PreviewObservationPermanent || outcome.Code != v1.PreviewInvalidObserverResponse ||
		outcome.Observation != nil {
		t.Fatalf("oversized response outcome = %#v", outcome)
	}
}

func TestObserveRejectsOutOfContractHTTPStatus(t *testing.T) {
	registry, _ := observerRegistry(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(600)
	}))
	outcome := registry.Observe(context.Background(), completeObservationOperation())
	if outcome.Kind != v1.PreviewObservationPermanent || outcome.Code != v1.PreviewInvalidObserverResponse {
		t.Fatalf("invalid HTTP status outcome = %#v", outcome)
	}
}

func TestObserveClassifiesTLSVerificationWithoutLeakingCertificateDetails(t *testing.T) {
	registry, _ := observerRegistry(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	for key, compiled := range registry.profiles {
		compiled.trustedCAFile = ""
		registry.profiles[key] = compiled
	}
	outcome := registry.Observe(context.Background(), completeObservationOperation())
	if outcome.Kind != v1.PreviewObservationRetryable || outcome.Code != v1.PreviewTLSVerificationFailed ||
		outcome.Observation != nil {
		t.Fatalf("TLS verification outcome = %#v", outcome)
	}
}

func TestObserveReturnsControlledFailureForMissingProfile(t *testing.T) {
	registry, err := NewRegistry(nil, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := registry.Observe(context.Background(), completeObservationOperation())
	if outcome.Kind != v1.PreviewObservationPermanent || outcome.Code != v1.PreviewProfileUnavailable {
		t.Fatalf("missing profile outcome = %#v", outcome)
	}
}
