package updater

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/updatecatalog"
)

func throwawayTrustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func trustDocumentFor(t *testing.T, now time.Time, keys map[string]*ecdsa.PrivateKey) []byte {
	t.Helper()
	entries := make([]map[string]any, 0, len(keys))
	for id, key := range keys {
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, map[string]any{"key_id": id, "public_key_b64": base64.StdEncoding.EncodeToString(der), "not_before": now.Add(-time.Hour), "not_after": now.Add(24 * time.Hour)})
	}
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "signature_threshold": 1, "keys": entries})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func releaseManifestAt(now time.Time) updatecatalog.Manifest {
	body := []byte("archive")
	sum := sha256.Sum256(body)
	return updatecatalog.Manifest{Schema: updatecatalog.SchemaV2, Channel: "stable", Sequence: 1, ReleaseID: "release-1", Version: "1.0.0", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), MinRunner: "0.1.0", MinUpdater: "0.1.0", Artifacts: []updatecatalog.Artifact{{OS: "linux", Arch: "amd64", Format: "tar.gz", URL: "https://release.example.test/release-1/archive.tgz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}}}
}

func latestFromServedEnvelope(t *testing.T, now time.Time, trust updatecatalog.TrustPolicy, envelope []byte) error {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(envelope) }))
	defer server.Close()
	catalog := &VerifiedCatalog{ManifestURL: server.URL, AllowedOrigins: []string{server.URL}, Trust: trust, Client: server.Client(), Clock: &fakeClock{now: now}}
	_, err := catalog.Latest(context.Background(), CatalogQuery{Channel: "stable", OS: "linux", Arch: "amd64", RunnerVersion: "0.1.0", UpdaterVersion: "0.1.0"})
	return err
}

func TestParseTrustDocumentRefusesAnEmptyTrustList(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"empty keys":           `{"schema_version":1,"signature_threshold":1,"keys":[]}`,
		"zero threshold":       `{"schema_version":1,"signature_threshold":0,"keys":[]}`,
		"missing keys":         `{"schema_version":1,"signature_threshold":1}`,
		"null keys":            `{"schema_version":1,"signature_threshold":1,"keys":null}`,
		"threshold above keys": `{"schema_version":1,"signature_threshold":2,"keys":[]}`,
	} {
		if _, err := ParseTrustDocument([]byte(raw)); !errors.Is(err, ErrTrustEmpty) && !errors.Is(err, ErrTrustInvalid) {
			t.Fatalf("%s: got %v, want an empty or invalid trust refusal", name, err)
		}
	}
	if _, err := ParseTrustDocument([]byte(`{"schema_version":1,"signature_threshold":1,"keys":[]}`)); !errors.Is(err, ErrTrustEmpty) {
		t.Fatalf("empty key list got %v, want ErrTrustEmpty", err)
	}
}

func TestShippedUpdateTrustIsRefusedUntilProvisioned(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "update-trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := ParseTrustDocument(raw)
	if err == nil {
		if len(trust.Keys) == 0 || trust.Threshold < 1 {
			t.Fatalf("shipped trust parsed without keys: %+v", trust)
		}
		return
	}
	if !errors.Is(err, ErrTrustEmpty) {
		t.Fatalf("shipped trust got %v, want ErrTrustEmpty while unprovisioned", err)
	}
	path := filepath.Join(t.TempDir(), "update-trust.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustFile(path, false); !errors.Is(err, ErrTrustEmpty) {
		t.Fatalf("LoadTrustFile on the shipped trust got %v, want ErrTrustEmpty", err)
	}
}

func TestParseTrustDocumentRejectsNonP256AndBrokenWindows(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustDocument(trustDocumentFor(t, now, map[string]*ecdsa.PrivateKey{"key-384": p384})); !errors.Is(err, ErrTrustInvalid) {
		t.Fatalf("P-384 trust key got %v", err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&throwawayTrustKey(t).PublicKey)
	encoded := base64.StdEncoding.EncodeToString(der)
	for name, window := range map[string]string{
		"missing not_after": `"not_before":"2026-01-01T00:00:00Z"`,
		"inverted":          `"not_before":"2027-01-01T00:00:00Z","not_after":"2026-01-01T00:00:00Z"`,
	} {
		raw := `{"schema_version":1,"signature_threshold":1,"keys":[{"key_id":"key-1","public_key_b64":"` + encoded + `",` + window + `}]}`
		if _, err := ParseTrustDocument([]byte(raw)); !errors.Is(err, ErrTrustInvalid) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	trust, err := ParseTrustDocument(trustDocumentFor(t, now, map[string]*ecdsa.PrivateKey{"key-1": throwawayTrustKey(t)}))
	if err != nil || trust.Threshold != 1 || len(trust.Keys) != 1 || trust.Keys[0].KeyID != "key-1" {
		t.Fatalf("valid trust got %+v, %v", trust, err)
	}
}

func TestVerifiedCatalogRefusesUnsignedAndWronglySignedUpdates(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	trusted, impostor := throwawayTrustKey(t), throwawayTrustKey(t)
	trust, err := ParseTrustDocument(trustDocumentFor(t, now, map[string]*ecdsa.PrivateKey{"release-key": trusted}))
	if err != nil {
		t.Fatal(err)
	}
	manifest := releaseManifestAt(now)

	genuine, err := updatecatalog.Sign(manifest, []updatecatalog.Signer{{KeyID: "release-key", PrivateKey: trusted}})
	if err != nil {
		t.Fatal(err)
	}
	if err := latestFromServedEnvelope(t, now, trust, genuine); err != nil {
		t.Fatalf("genuine update refused: %v", err)
	}

	payload, err := updatecatalog.CanonicalPayload(manifest)
	if err != nil {
		t.Fatal(err)
	}
	unsigned, _ := json.Marshal(map[string]any{"payload": base64.StdEncoding.EncodeToString(payload), "signatures": []any{}})
	if err := latestFromServedEnvelope(t, now, trust, unsigned); err == nil {
		t.Fatal("unsigned update accepted")
	}
	payloadOnly, _ := json.Marshal(map[string]any{"payload": base64.StdEncoding.EncodeToString(payload)})
	if err := latestFromServedEnvelope(t, now, trust, payloadOnly); err == nil {
		t.Fatal("update without a signatures field accepted")
	}

	impostorSameID, err := updatecatalog.Sign(manifest, []updatecatalog.Signer{{KeyID: "release-key", PrivateKey: impostor}})
	if err != nil {
		t.Fatal(err)
	}
	if err := latestFromServedEnvelope(t, now, trust, impostorSameID); !errors.Is(err, updatecatalog.ErrInsufficientSignatures) {
		t.Fatalf("update signed by an untrusted key under the trusted key id got %v", err)
	}
	impostorOwnID, err := updatecatalog.Sign(manifest, []updatecatalog.Signer{{KeyID: "impostor-key", PrivateKey: impostor}})
	if err != nil {
		t.Fatal(err)
	}
	if err := latestFromServedEnvelope(t, now, trust, impostorOwnID); !errors.Is(err, updatecatalog.ErrInsufficientSignatures) {
		t.Fatalf("update signed by an unknown key got %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(genuine, &envelope); err != nil {
		t.Fatal(err)
	}
	tamperedPayload := strings.Replace(string(payload), `"version":"1.0.0"`, `"version":"1.0.1"`, 1)
	if tamperedPayload == string(payload) {
		t.Fatal("tamper fixture did not change the payload")
	}
	envelope["payload"] = base64.StdEncoding.EncodeToString([]byte(tamperedPayload))
	tampered, _ := json.Marshal(envelope)
	if err := latestFromServedEnvelope(t, now, trust, tampered); !errors.Is(err, updatecatalog.ErrInsufficientSignatures) {
		t.Fatalf("update whose payload changed after signing got %v", err)
	}

	if err := latestFromServedEnvelope(t, now, updatecatalog.TrustPolicy{}, genuine); err == nil {
		t.Fatal("genuine update accepted with an empty trust list")
	}
	if err := latestFromServedEnvelope(t, now, updatecatalog.TrustPolicy{Threshold: 1}, genuine); err == nil {
		t.Fatal("genuine update accepted with a threshold and no trusted keys")
	}
}
