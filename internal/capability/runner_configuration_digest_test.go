package capability

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectReportsTheRunningConfigurationDigest(t *testing.T) {
	digest := strings.Repeat("a", 64)
	d := fakeDetector()
	d.RunnerConfigurationDigest = digest
	m := d.Collect(context.Background())
	if m.RunnerConfigurationDigest != digest {
		t.Fatalf("RunnerConfigurationDigest = %q", m.RunnerConfigurationDigest)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"runner_configuration_digest":"`+digest+`","signature"`) {
		t.Fatalf("digest must be serialized immediately before the signature: %s", raw)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		t.Fatal(err)
	}
	m.Signature = ""
	payload, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if !strings.Contains(string(payload), digest) || !ecdsa.VerifyASN1(&key.PublicKey, sum[:], signature) {
		t.Fatal("signature does not cover the running configuration digest")
	}
}

func TestCollectOmitsAMissingOrInvalidConfigurationDigest(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		d := fakeDetector()
		d.RunnerConfigurationDigest = value
		m := d.Collect(context.Background())
		if m.RunnerConfigurationDigest != "" {
			t.Fatalf("value %q advertised as %q", value, m.RunnerConfigurationDigest)
		}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "runner_configuration_digest") {
			t.Fatalf("legacy manifest gained the optional field: %s", raw)
		}
	}
}
