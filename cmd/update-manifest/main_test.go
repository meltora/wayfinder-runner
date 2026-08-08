package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/app-runner/internal/updatecatalog"
)

func TestGenerateAndAssembleDetachedKMSSignature(t *testing.T) {
	dir := t.TempDir()
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(dist, 0o700); err != nil {
		t.Fatal(err)
	}
	records := make([]map[string]any, 0, 2)
	for _, arch := range []string{"amd64", "arm64"} {
		name := "our-runner-update_1.4.0_linux_" + arch + ".tar.gz"
		path := filepath.Join(dist, name)
		contents := []byte("release archive " + arch)
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		records = append(records, map[string]any{
			"name": name, "path": path, "type": "Archive", "goos": "linux", "goarch": arch,
			"extra": map[string]any{"Format": "tar.gz", "ID": "linux-update", "Checksum": "sha256:" + hex.EncodeToString(digest[:])},
		})
	}
	artifactJSON, _ := json.Marshal(records)
	artifactsPath := filepath.Join(dist, "artifacts.json")
	if err := os.WriteFile(artifactsPath, artifactJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(dir, "payload.json")
	digestPath := filepath.Join(dir, "digest.bin")
	if err := run([]string{
		"generate", "--artifacts", artifactsPath, "--dist", dist,
		"--base-url", "https://github.com/acme/runner/releases/download/v1.4.0",
		"--channel", "stable", "--sequence", "104", "--release-id", "v1.4.0", "--version", "1.4.0",
		"--issued-at", "2026-08-06T12:00:00Z", "--expires-at", "2026-08-13T12:00:00Z",
		"--min-runner", "1.2.0", "--min-updater", "1.0.0",
		"--payload-out", payloadPath, "--digest-out", digestPath,
	}, os.Stdout); err != nil {
		t.Fatal(err)
	}
	digest, err := os.ReadFile(digestPath)
	if err != nil || len(digest) != sha256.Size {
		t.Fatalf("digest length=%d err=%v", len(digest), err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	badSignaturePath := filepath.Join(dir, "bad-signature.b64")
	if err := os.WriteFile(badSignaturePath, []byte("not-base64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"assemble", "--payload", payloadPath, "--signature", "release-a=" + badSignaturePath, "--output", filepath.Join(dir, "bad-envelope")}, os.Stdout); err == nil {
		t.Fatal("non-base64 detached signature accepted")
	}
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest)
	if err != nil {
		t.Fatal(err)
	}
	signaturePath := filepath.Join(dir, "signature.b64")
	if err := os.WriteFile(signaturePath, []byte(base64.StdEncoding.EncodeToString(signature)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envelopePath := filepath.Join(dir, "manifest.json")
	if err := run([]string{"assemble", "--payload", payloadPath, "--signature", "release-a=" + signaturePath, "--output", envelopePath}, os.Stdout); err != nil {
		t.Fatal(err)
	}
	envelope, _ := os.ReadFile(envelopePath)
	verified, err := updatecatalog.ParseAndVerify(envelope, updatecatalog.VerifyPolicy{
		Channel: "stable", OS: "linux", Arch: "amd64", RunnerVersion: "1.3.0", UpdaterVersion: "1.0.0",
		Now: time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC), ClockSkew: time.Minute,
		MaxLifetime: 7 * 24 * time.Hour,
		Trust: updatecatalog.TrustPolicy{Threshold: 1, Keys: []updatecatalog.TrustedKey{{
			KeyID: "release-a", PublicKey: &key.PublicKey,
			NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		}}},
	})
	if err != nil || verified.Manifest.Sequence != 104 {
		t.Fatalf("verify=%+v err=%v", verified, err)
	}
}

func TestCommandRejectsUnsafeOrIncompleteInputs(t *testing.T) {
	if err := run(nil, os.Stdout); err == nil {
		t.Fatal("missing command accepted")
	}
	if err := run([]string{"generate", "--sequence", "0"}, os.Stdout); err == nil {
		t.Fatal("zero sequence accepted")
	}
	if err := run([]string{"future"}, os.Stdout); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := run([]string{"generate", "--bad-flag"}, os.Stdout); err == nil {
		t.Fatal("unknown generate flag accepted")
	}
	if err := run([]string{"assemble"}, os.Stdout); err == nil {
		t.Fatal("incomplete assemble command accepted")
	}
	if _, err := parseWholeSecondTime("issued-at", "2026-08-06T12:00:00+02:00"); err == nil {
		t.Fatal("non-UTC timestamp accepted")
	}
	if _, err := parseWholeSecondTime("issued-at", "bogus"); err == nil {
		t.Fatal("malformed timestamp accepted")
	}
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload")
	signature := filepath.Join(dir, "sig")
	_ = os.WriteFile(payload, []byte("{}"), 0o600)
	_ = os.WriteFile(signature, []byte("not-base64"), 0o600)
	if err := run([]string{"assemble", "--payload", payload, "--signature", "key-a=" + signature, "--output", filepath.Join(dir, "out")}, os.Stdout); err == nil {
		t.Fatal("invalid payload/signature accepted")
	}
	if err := run([]string{"assemble", "--payload", payload, "--signature", "missing-separator", "--output", filepath.Join(dir, "out")}, os.Stdout); err == nil {
		t.Fatal("malformed signature argument accepted")
	}
	if err := atomicOutput("", []byte("x"), 0o600); err == nil {
		t.Fatal("empty output path accepted")
	}
}
