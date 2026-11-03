package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTrustFixture(t *testing.T, threshold int, windows ...[2]time.Time) string {
	t.Helper()
	keys := make([]map[string]any, 0, len(windows))
	for i, window := range windows {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, map[string]any{"key_id": "release-" + string(rune('a'+i)), "public_key_b64": base64.StdEncoding.EncodeToString(der), "not_before": window[0], "not_after": window[1]})
	}
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "signature_threshold": threshold, "keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "update-trust.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckTrustRequiresProvisionedKeysUnlessEmptyIsAllowed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	current := [2]time.Time{now.Add(-time.Hour), now.Add(365 * 24 * time.Hour)}
	expired := [2]time.Time{now.Add(-48 * time.Hour), now.Add(-time.Hour)}

	empty := filepath.Join(t.TempDir(), "update-trust.json")
	if err := os.WriteFile(empty, []byte(`{"schema_version":1,"signature_threshold":1,"keys":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"check-trust", empty}, &out); err == nil || !strings.Contains(err.Error(), "no keys") {
		t.Fatalf("empty trust without --allow-empty got %v", err)
	}
	out.Reset()
	if err := run([]string{"check-trust", "--allow-empty", empty}, &out); err != nil || !strings.Contains(out.String(), "empty") {
		t.Fatalf("empty trust with --allow-empty got %v, %q", err, out.String())
	}

	valid := writeTrustFixture(t, 1, current)
	out.Reset()
	if err := run([]string{"check-trust", valid}, &out); err != nil || !strings.Contains(out.String(), "1 keys, threshold 1") {
		t.Fatalf("provisioned trust got %v, %q", err, out.String())
	}
	out.Reset()
	if err := run([]string{"check-trust", "--allow-empty", valid}, &out); err != nil {
		t.Fatalf("provisioned trust with --allow-empty got %v", err)
	}

	if err := run([]string{"check-trust", writeTrustFixture(t, 1, expired)}, &out); err == nil {
		t.Fatal("trust whose only key has expired accepted")
	}
	if err := run([]string{"check-trust", writeTrustFixture(t, 2, current, expired)}, &out); err == nil {
		t.Fatal("trust with fewer current keys than the threshold accepted")
	}
	if err := run([]string{"check-trust", writeTrustFixture(t, 2, current, current)}, &out); err != nil {
		t.Fatalf("two current keys at threshold 2 got %v", err)
	}

	malformed := filepath.Join(t.TempDir(), "update-trust.json")
	if err := os.WriteFile(malformed, []byte(`{"schema_version":1,"signature_threshold":1,"keys":[],"extra":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check-trust", "--allow-empty", malformed}, &out); err == nil {
		t.Fatal("malformed trust accepted with --allow-empty")
	}
	for _, args := range [][]string{{"check-trust"}, {"check-trust", "--allow-empty"}, {"check-trust", empty, valid}, {"check-trust", filepath.Join(t.TempDir(), "missing.json")}} {
		if err := run(args, &out); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

func writeRevokedTrustFixture(t *testing.T, window [2]time.Time, revokedAt time.Time) string {
	t.Helper()
	path := writeTrustFixture(t, 1, window)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["keys"].([]any)[0].(map[string]any)["revoked_at"] = revokedAt
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckTrustCountsOnlyKeysValidNow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	current := [2]time.Time{now.Add(-time.Hour), now.Add(365 * 24 * time.Hour)}
	notYetValid := [2]time.Time{now.Add(time.Hour), now.Add(365 * 24 * time.Hour)}
	var out bytes.Buffer

	if err := run([]string{"check-trust", writeTrustFixture(t, 1, notYetValid)}, &out); err == nil || !strings.Contains(err.Error(), "currently valid") {
		t.Fatalf("trust whose only key is not yet valid got %v", err)
	}
	if err := run([]string{"check-trust", writeTrustFixture(t, 2, current, notYetValid)}, &out); err == nil {
		t.Fatal("trust with one current and one future key accepted at threshold 2")
	}
	if err := run([]string{"check-trust", writeRevokedTrustFixture(t, current, now.Add(-time.Minute))}, &out); err == nil || !strings.Contains(err.Error(), "currently valid") {
		t.Fatalf("trust whose only key is revoked got %v", err)
	}
	out.Reset()
	if err := run([]string{"check-trust", writeRevokedTrustFixture(t, current, now.Add(24*time.Hour))}, &out); err != nil || !strings.Contains(out.String(), "1 currently valid") {
		t.Fatalf("trust whose key is revoked only in the future got %v, %q", err, out.String())
	}
}

func TestShippedTrustPassesOnlyTheChannelGateItIsMeantFor(t *testing.T) {
	shipped := filepath.Join("..", "..", "packaging", "update-trust.json")
	var out bytes.Buffer
	if err := run([]string{"check-trust", "--allow-empty", shipped}, &out); err != nil {
		t.Fatalf("shipped trust is malformed: %v", err)
	}
	if strings.Contains(out.String(), "empty") {
		if err := run([]string{"check-trust", shipped}, &out); err == nil {
			t.Fatal("the unprovisioned shipped trust passed the production gate")
		}
	}
}
