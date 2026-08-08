package updatecatalog

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestManifestFieldValidation(t *testing.T) {
	base := validManifest()
	cases := map[string]func(*Manifest){
		"schema":           func(m *Manifest) { m.Schema = "v1" },
		"channel":          func(m *Manifest) { m.Channel = "Stable" },
		"sequence":         func(m *Manifest) { m.Sequence = 0 },
		"release id":       func(m *Manifest) { m.ReleaseID = "../release" },
		"version":          func(m *Manifest) { m.Version = "v1.4" },
		"min runner":       func(m *Manifest) { m.MinRunner = "1" },
		"min updater":      func(m *Manifest) { m.MinUpdater = "latest" },
		"issued precision": func(m *Manifest) { m.IssuedAt = m.IssuedAt.Add(time.Nanosecond) },
		"expiry before":    func(m *Manifest) { m.ExpiresAt = m.IssuedAt },
		"no artifacts":     func(m *Manifest) { m.Artifacts = nil },
		"bad os":           func(m *Manifest) { m.Artifacts[0].OS = "Linux" },
		"bad arch":         func(m *Manifest) { m.Artifacts[0].Arch = "../amd64" },
		"bad format":       func(m *Manifest) { m.Artifacts[0].Format = "" },
		"http url":         func(m *Manifest) { m.Artifacts[0].URL = "http://releases.example.test/file" },
		"url credentials":  func(m *Manifest) { m.Artifacts[0].URL = "https://user:pass@releases.example.test/file" },
		"url query":        func(m *Manifest) { m.Artifacts[0].URL += "?token=x" },
		"url fragment":     func(m *Manifest) { m.Artifacts[0].URL += "#x" },
		"moving url":       func(m *Manifest) { m.Artifacts[0].URL = "https://releases.example.test/latest/file" },
		"digest":           func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("A", 64) },
		"size zero":        func(m *Manifest) { m.Artifacts[0].Size = 0 },
		"size excessive":   func(m *Manifest) { m.Artifacts[0].Size = MaxArtifactSize + 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := base
			manifest.Artifacts = append([]Artifact(nil), base.Artifacts...)
			mutate(&manifest)
			if _, err := CanonicalPayload(manifest); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("got %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestStableRejectsPrerelease(t *testing.T) {
	key := newKey(t)
	manifest := validManifest()
	manifest.Version = "1.4.0-rc.1"
	if _, err := Sign(manifest, []Signer{{KeyID: "key-a", PrivateKey: key}}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("got %v, want prerelease rejection", err)
	}

	manifest.Channel = "beta"
	for i := range manifest.Artifacts {
		manifest.Artifacts[i].URL = strings.Replace(manifest.Artifacts[i].URL, "1.4.0", "1.4.0-rc.1", 1)
	}
	policy := policyFor(key)
	policy.Channel = "beta"
	if _, err := ParseAndVerify(signWith(t, manifest, key), policy); err != nil {
		t.Fatalf("beta prerelease rejected: %v", err)
	}
}

func TestPolicyTimeWindowAndSkew(t *testing.T) {
	key := newKey(t)
	base := validManifest()
	cases := map[string]func(*Manifest, *VerifyPolicy){
		"future": func(m *Manifest, p *VerifyPolicy) {
			m.IssuedAt = p.Now.Add(p.ClockSkew + time.Second)
			m.ExpiresAt = m.IssuedAt.Add(time.Hour)
		},
		"expired": func(m *Manifest, p *VerifyPolicy) {
			m.IssuedAt = p.Now.Add(-2 * time.Hour)
			m.ExpiresAt = p.Now.Add(-p.ClockSkew - time.Second)
		},
		"lifetime": func(m *Manifest, p *VerifyPolicy) { m.ExpiresAt = m.IssuedAt.Add(p.MaxLifetime + time.Second) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := base
			manifest.Artifacts = append([]Artifact(nil), base.Artifacts...)
			policy := policyFor(key)
			mutate(&manifest, &policy)
			if _, err := ParseAndVerify(signWith(t, manifest, key), policy); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("got %v", err)
			}
		})
	}

	manifest := base
	manifest.IssuedAt = testNow.Add(-10 * time.Minute)
	manifest.ExpiresAt = testNow.Add(-policyFor(key).ClockSkew)
	if _, err := ParseAndVerify(signWith(t, manifest, key), policyFor(key)); err != nil {
		t.Fatalf("boundary within skew rejected: %v", err)
	}
}

func TestVersionAndChannelGates(t *testing.T) {
	key := newKey(t)
	encoded := signWith(t, validManifest(), key)
	cases := map[string]func(*VerifyPolicy){
		"channel mismatch": func(p *VerifyPolicy) { p.Channel = "beta" },
		"runner too old":   func(p *VerifyPolicy) { p.RunnerVersion = "0.9.9" },
		"updater too old":  func(p *VerifyPolicy) { p.UpdaterVersion = "1.0.9" },
		"invalid runner":   func(p *VerifyPolicy) { p.RunnerVersion = "dev" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			policy := policyFor(key)
			mutate(&policy)
			if _, err := ParseAndVerify(encoded, policy); !errors.Is(err, ErrVersionGate) {
				t.Fatalf("got %v, want ErrVersionGate", err)
			}
		})
	}
}

func TestReplayEquivocationAndIdempotency(t *testing.T) {
	key := newKey(t)
	manifest := validManifest()
	encoded := signWith(t, manifest, key)
	verified, err := ParseAndVerify(encoded, policyFor(key))
	if err != nil {
		t.Fatal(err)
	}

	replayPolicy := policyFor(key)
	replayPolicy.Current = State{Sequence: manifest.Sequence + 1, ReleaseID: "later", PayloadSHA256: strings.Repeat("a", 64)}
	if _, err := ParseAndVerify(encoded, replayPolicy); !errors.Is(err, ErrReplay) {
		t.Fatalf("got %v, want ErrReplay", err)
	}

	idempotent := policyFor(key)
	idempotent.Current = State{Sequence: manifest.Sequence, ReleaseID: manifest.ReleaseID, PayloadSHA256: verified.PayloadSHA256}
	seen, err := ParseAndVerify(encoded, idempotent)
	if err != nil || !seen.AlreadySeen {
		t.Fatalf("idempotent verify = %+v, %v", seen, err)
	}

	equivocation := policyFor(key)
	equivocation.Current = State{Sequence: manifest.Sequence, ReleaseID: "different", PayloadSHA256: strings.Repeat("b", 64)}
	if _, err := ParseAndVerify(encoded, equivocation); !errors.Is(err, ErrEquivocation) {
		t.Fatalf("got %v, want ErrEquivocation", err)
	}

	reusedRelease := manifest
	reusedRelease.Sequence++
	reusePolicy := policyFor(key)
	reusePolicy.Current = State{Sequence: manifest.Sequence, ReleaseID: manifest.ReleaseID, PayloadSHA256: verified.PayloadSHA256}
	if _, err := ParseAndVerify(signWith(t, reusedRelease, key), reusePolicy); !errors.Is(err, ErrEquivocation) {
		t.Fatalf("reused release id got %v, want ErrEquivocation", err)
	}

	missingDigest := policyFor(key)
	missingDigest.Current = State{Sequence: manifest.Sequence, ReleaseID: manifest.ReleaseID}
	if _, err := ParseAndVerify(encoded, missingDigest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("got %v, want invalid state", err)
	}
}

func TestTrustWindowsRevocationAndRotation(t *testing.T) {
	oldKey, newKey := newKey(t), newKey(t)
	encoded := signWith(t, validManifest(), oldKey, newKey)
	policy := policyFor(oldKey, newKey)
	policy.Trust.Threshold = 1
	revoked := testNow.Add(-time.Second)
	policy.Trust.Keys[0].RevokedAt = &revoked
	verified, err := ParseAndVerify(encoded, policy)
	if err != nil {
		t.Fatalf("rotation overlap failed: %v", err)
	}
	if len(verified.ValidKeyIDs) != 1 || verified.ValidKeyIDs[0] != "key-b" {
		t.Fatalf("valid keys = %v", verified.ValidKeyIDs)
	}

	policy.Trust.Threshold = 2
	if _, err := ParseAndVerify(encoded, policy); !errors.Is(err, ErrInsufficientSignatures) {
		t.Fatalf("revoked key counted: %v", err)
	}

	policy = policyFor(oldKey)
	policy.Trust.Keys[0].NotBefore = validManifest().IssuedAt.Add(time.Second)
	if _, err := ParseAndVerify(signWith(t, validManifest(), oldKey), policy); !errors.Is(err, ErrInsufficientSignatures) {
		t.Fatalf("not-yet-valid key counted: %v", err)
	}

	policy = policyFor(oldKey)
	policy.Trust.Keys[0].NotAfter = validManifest().IssuedAt.Add(-time.Second)
	if _, err := ParseAndVerify(signWith(t, validManifest(), oldKey), policy); !errors.Is(err, ErrInsufficientSignatures) {
		t.Fatalf("expired signing key counted: %v", err)
	}

	policy = policyFor(oldKey)
	policy.Trust.Keys[0].NotAfter = testNow.Add(-time.Second)
	if _, err := ParseAndVerify(signWith(t, validManifest(), oldKey), policy); !errors.Is(err, ErrInsufficientSignatures) {
		t.Fatalf("key expired at verification time counted: %v", err)
	}
}

func TestTrustAndPolicyRejectDuplicatesAndInvalidKeys(t *testing.T) {
	key := newKey(t)
	encoded := signWith(t, validManifest(), key)
	policy := policyFor(key)
	policy.Trust.Keys = append(policy.Trust.Keys, policy.Trust.Keys[0])
	if _, err := ParseAndVerify(encoded, policy); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("duplicate trusted key got %v", err)
	}

	policy = policyFor(key)
	policy.Trust.Threshold = 2
	if _, err := ParseAndVerify(encoded, policy); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("impossible threshold got %v", err)
	}

	digest, err := SigningDigest(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	aliasEnvelope, err := Assemble(validManifest(), []DetachedSignature{
		{KeyID: "key-a", Value: signature},
		{KeyID: "key-b", Value: signature},
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicateKeyPolicy := policyFor(key)
	aliasKey := duplicateKeyPolicy.Trust.Keys[0]
	aliasKey.KeyID = "key-b"
	duplicateKeyPolicy.Trust.Keys = append(duplicateKeyPolicy.Trust.Keys, aliasKey)
	duplicateKeyPolicy.Trust.Threshold = 2
	if _, err := ParseAndVerify(aliasEnvelope, duplicateKeyPolicy); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("same public key under two ids got %v", err)
	}
}

func TestTamperedPayloadAndMalformedSignatureRejected(t *testing.T) {
	key := newKey(t)
	encoded := signWith(t, validManifest(), key)
	var env struct {
		Payload    string      `json:"payload"`
		Signatures []Signature `json:"signatures"`
	}
	if err := json.Unmarshal(encoded, &env); err != nil {
		t.Fatal(err)
	}
	originalSignatures := append([]Signature(nil), env.Signatures...)
	env.Signatures[0].Value = "not-base64!"
	malformed, _ := json.Marshal(env)
	if _, err := ParseAndVerify(malformed, policyFor(key)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("bad signature got %v", err)
	}

	payload, _ := CanonicalPayload(validManifest())
	payload = []byte(strings.Replace(string(payload), `"min_runner":"1.0.0"`, `"min_runner":"1.0.1"`, 1))
	if _, err := ParseAndVerify(envelopeBytes(t, payload, originalSignatures), policyFor(key)); !errors.Is(err, ErrInsufficientSignatures) {
		t.Fatalf("tamper got %v", err)
	}
}
