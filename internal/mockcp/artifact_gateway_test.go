package mockcp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/identity"
)

func TestGatewayArtifactEndpointUsesMutualTLS(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := artifactGatewayClient(t, s)
	defer client.CloseIdleConnections()
	body := []byte("retained patch")
	digest := sha256.Sum256(body)
	base := strings.TrimSuffix(strings.Replace(s.WSURL(), "wss://", "https://", 1), "/v1/runners/stream")
	req, err := http.NewRequest(http.MethodPut, base+"/v1/artifacts/job-1/attempt-1/changes.patch", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Artifact-Digest", hex.EncodeToString(digest[:]))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("gateway upload status=%d", resp.StatusCode)
	}
	stored, ok := s.Artifact("job-1", "attempt-1", "changes.patch")
	if !ok || !bytes.Equal(stored, body) {
		t.Fatalf("stored=%q found=%t", stored, ok)
	}
}

func artifactGatewayClient(t *testing.T, s *Server) *http.Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := identity.NewCSR(key, "runner-artifact")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := s.CA.SignCSR(csr, "runner-artifact")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(s.CA.CertPEM)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{cert},
	}}}
}
