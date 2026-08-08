package updater

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/updatecatalog"
)

func TestHTTPArtifactFetcherExactDigestSizeAndOrigin(t *testing.T) {
	t.Parallel()
	body := []byte("signed artifact")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	sum := sha256.Sum256(body)
	fetcher := &HTTPArtifactFetcher{AllowedOrigins: []string{server.URL}, Client: server.Client(), MaxSize: 1024}
	var dst bytes.Buffer
	if err := fetcher.Download(context.Background(), server.URL+"/release", int64(len(body)), hex.EncodeToString(sum[:]), &dst); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dst.Bytes(), body) {
		t.Fatalf("body=%q", dst.Bytes())
	}
	for name, call := range map[string]func() error{"bad digest": func() error {
		return fetcher.Download(context.Background(), server.URL+"/release", int64(len(body)), strings.Repeat("0", 64), &bytes.Buffer{})
	}, "bad size": func() error {
		return fetcher.Download(context.Background(), server.URL+"/release", int64(len(body))+1, hex.EncodeToString(sum[:]), &bytes.Buffer{})
	}, "too large": func() error {
		return fetcher.Download(context.Background(), server.URL+"/release", 2048, hex.EncodeToString(sum[:]), &bytes.Buffer{})
	}, "origin": func() error {
		return fetcher.Download(context.Background(), "https://attacker.invalid/x", 1, strings.Repeat("0", 64), &bytes.Buffer{})
	}} {
		if err := call(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestStrictHTTPRedirectRejectsDifferentOrigin(t *testing.T) {
	t.Parallel()
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	client := source.Client()
	client.Transport = source.Client().Transport
	fetcher := &HTTPArtifactFetcher{AllowedOrigins: []string{source.URL}, Client: client, MaxSize: 10}
	if err := fetcher.Download(context.Background(), source.URL, 1, strings.Repeat("0", 64), &bytes.Buffer{}); err == nil {
		t.Fatal("cross-origin redirect accepted")
	}
}

func TestLoadTrustFileStrictAndVerifiedCatalog(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	doc := map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": []map[string]any{{"key_id": "key-1", "public_key_b64": base64.StdEncoding.EncodeToString(der), "not_before": now.Add(-time.Hour), "not_after": now.Add(time.Hour)}}}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	trust, err := LoadTrustFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	artifactBody := []byte("archive")
	sum := sha256.Sum256(artifactBody)
	manifest := updatecatalog.Manifest{Schema: updatecatalog.SchemaV2, Channel: "stable", Sequence: 1, ReleaseID: "release-1", Version: "1.0.0", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), MinRunner: "0.1.0", MinUpdater: "0.1.0", Artifacts: []updatecatalog.Artifact{{OS: "linux", Arch: "amd64", Format: "tar.gz", URL: "https://release.example.test/release-1/archive.tgz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(artifactBody))}}}
	envelope, err := updatecatalog.Sign(manifest, []updatecatalog.Signer{{KeyID: "key-1", PrivateKey: key}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(envelope) }))
	defer server.Close()
	catalog := &VerifiedCatalog{ManifestURL: server.URL, AllowedOrigins: []string{server.URL}, Trust: trust, Client: server.Client(), Clock: &fakeClock{now: now}}
	release, err := catalog.Latest(context.Background(), CatalogQuery{Channel: "stable", OS: "linux", Arch: "amd64", RunnerVersion: "0.1.0", UpdaterVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if release.ReleaseID != "release-1" || release.URL != manifest.Artifacts[0].URL {
		t.Fatalf("release=%+v", release)
	}
	duplicate := []byte(`{"schema_version":1,"schema_version":1,"signature_threshold":1,"keys":[]}`)
	if err := os.WriteFile(path, duplicate, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustFile(path, false); err == nil {
		t.Fatal("duplicate trust field accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustFile(path, false); err == nil {
		t.Fatal("public trust file accepted")
	}
}

func TestValidateHTTPSURLRejectsUnsafeForms(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"http://example.test", "https://user@example.test", "https://example.test/#fragment", "relative"} {
		if _, err := validateHTTPSURL(raw, []string{"https://example.test"}); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := validateHTTPSURL("https://example.test/path", []string{"https://example.test"}); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifiedCatalogFailsClosedAtEveryHTTPBoundary(t *testing.T) {
	t.Parallel()
	query := CatalogQuery{Channel: "stable", OS: "linux", Arch: "amd64", RunnerVersion: "0.1.0", UpdaterVersion: "0.1.0"}
	empty := &VerifiedCatalog{ManifestURL: "https://release.test/manifest", AllowedOrigins: []string{"https://release.test"}}
	if _, err := empty.Latest(context.Background(), query); err == nil {
		t.Fatal("empty trust accepted")
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	trust := updatecatalog.TrustPolicy{Threshold: 1, Keys: []updatecatalog.TrustedKey{{KeyID: "key", PublicKey: &key.PublicKey, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}}}
	for name, response := range map[string]*http.Response{"status": {StatusCode: 500, Body: io.NopCloser(strings.NewReader(""))}, "oversize": {StatusCode: 200, ContentLength: maxCatalogBytes + 1, Body: io.NopCloser(strings.NewReader(""))}, "malformed": {StatusCode: 200, ContentLength: -1, Body: io.NopCloser(strings.NewReader("{}"))}} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { copy := *response; return &copy, nil })}
			catalog := &VerifiedCatalog{ManifestURL: "https://release.test/manifest", AllowedOrigins: []string{"https://release.test"}, Trust: trust, Client: client}
			if _, err := catalog.Latest(context.Background(), query); err == nil {
				t.Fatal("bad manifest response accepted")
			}
		})
	}
	empty.Trust = trust
	empty.ManifestURL = "https://attacker.test/manifest"
	if _, err := empty.Latest(context.Background(), query); err == nil {
		t.Fatal("disallowed manifest origin accepted")
	}
}

func TestLoadTrustFileRejectsMalformedKeysAndPaths(t *testing.T) {
	t.Parallel()
	if _, err := LoadTrustFile("relative", false); err == nil {
		t.Fatal("relative trust accepted")
	}
	path := filepath.Join(t.TempDir(), "trust.json")
	if _, err := LoadTrustFile(path, false); err == nil {
		t.Fatal("missing trust accepted")
	}
	bad := `{"schema_version":1,"signature_threshold":1,"keys":[{"key_id":"key","public_key_b64":"bm90LWRlcg==","not_before":"2026-01-01T00:00:00Z","not_after":"2027-01-01T00:00:00Z"}]}`
	bad = strings.ReplaceAll(bad, `\"`, `"`)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustFile(path, false); err == nil {
		t.Fatal("invalid public key accepted")
	}
	if err := validateRootDirectory(t.TempDir(), false); err == nil && os.Geteuid() != 0 {
		t.Fatal("non-root directory accepted")
	}
	if _, err := (OSPeerProvider{}).Peer(nil); err == nil {
		t.Fatal("non-linux peer credentials accepted")
	}
}
