package updatecatalog

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func validManifest() Manifest {
	digest := sha256.Sum256([]byte("runner-linux-amd64"))
	return Manifest{
		Schema:     SchemaV2,
		Channel:    "stable",
		Sequence:   42,
		ReleaseID:  "release-2026-08-06-001",
		Version:    "1.4.0",
		IssuedAt:   testNow.Add(-time.Minute),
		ExpiresAt:  testNow.Add(23 * time.Hour),
		MinRunner:  "1.0.0",
		MinUpdater: "1.1.0",
		Artifacts: []Artifact{
			{
				OS:     "linux",
				Arch:   "amd64",
				Format: "tar.gz",
				URL:    "https://releases.example.test/download/1.4.0/our-runner_linux_amd64.tar.gz",
				SHA256: hex.EncodeToString(digest[:]),
				Size:   int64(len("runner-linux-amd64")),
			},
			{
				OS:     "darwin",
				Arch:   "arm64",
				Format: "tar.gz",
				URL:    "https://releases.example.test/download/1.4.0/our-runner_darwin_arm64.tar.gz",
				SHA256: strings.Repeat("b", 64),
				Size:   123,
			},
		},
	}
}

func policyFor(keys ...*ecdsa.PrivateKey) VerifyPolicy {
	trusted := make([]TrustedKey, 0, len(keys))
	for i, key := range keys {
		trusted = append(trusted, TrustedKey{
			KeyID:     "key-" + string(rune('a'+i)),
			PublicKey: &key.PublicKey,
			NotBefore: testNow.Add(-24 * time.Hour),
			NotAfter:  testNow.Add(365 * 24 * time.Hour),
		})
	}
	return VerifyPolicy{
		Channel:        "stable",
		OS:             "linux",
		Arch:           "amd64",
		RunnerVersion:  "1.3.0",
		UpdaterVersion: "1.2.0",
		Now:            testNow,
		ClockSkew:      2 * time.Minute,
		MaxLifetime:    48 * time.Hour,
		Trust:          TrustPolicy{Threshold: len(keys), Keys: trusted},
	}
}

func signWith(t *testing.T, manifest Manifest, keys ...*ecdsa.PrivateKey) []byte {
	t.Helper()
	signers := make([]Signer, 0, len(keys))
	for i, key := range keys {
		signers = append(signers, Signer{KeyID: "key-" + string(rune('a'+i)), PrivateKey: key})
	}
	encoded, err := Sign(manifest, signers)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return encoded
}

func TestSignAndVerifyThresholdManifest(t *testing.T) {
	keyA, keyB, keyC := newKey(t), newKey(t), newKey(t)
	encoded := signWith(t, validManifest(), keyA, keyB, keyC)
	policy := policyFor(keyA, keyB, keyC)
	policy.Trust.Threshold = 2

	verified, err := ParseAndVerify(encoded, policy)
	if err != nil {
		t.Fatalf("ParseAndVerify: %v", err)
	}
	if verified.Manifest.ReleaseID != "release-2026-08-06-001" || verified.Artifact.OS != "linux" {
		t.Fatalf("unexpected verified release: %+v", verified)
	}
	if len(verified.ValidKeyIDs) != 3 || verified.PayloadSHA256 == "" || verified.AlreadySeen {
		t.Fatalf("unexpected verification metadata: %+v", verified)
	}

	payload, err := CanonicalPayload(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := PayloadDigest(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	if verified.PayloadSHA256 != hex.EncodeToString(digest[:]) || sha256.Sum256(payload) != digest {
		t.Fatal("payload digest is not deterministic")
	}
}

func TestThresholdRequiresDistinctTrustedSignatures(t *testing.T) {
	keyA, keyB := newKey(t), newKey(t)
	encoded := signWith(t, validManifest(), keyA)
	policy := policyFor(keyA, keyB)
	policy.Trust.Threshold = 2
	if _, err := ParseAndVerify(encoded, policy); !errors.Is(err, ErrInsufficientSignatures) {
		t.Fatalf("got %v, want ErrInsufficientSignatures", err)
	}

	var env map[string]any
	if err := json.Unmarshal(encoded, &env); err != nil {
		t.Fatal(err)
	}
	sigs := env["signatures"].([]any)
	env["signatures"] = append(sigs, sigs[0])
	duplicate, _ := json.Marshal(env)
	if _, err := ParseAndVerify(duplicate, policy); !errors.Is(err, ErrMalformed) {
		t.Fatalf("duplicate signature got %v, want ErrMalformed", err)
	}
}

func TestStrictEnvelopeAndCanonicalPayload(t *testing.T) {
	key := newKey(t)
	valid := signWith(t, validManifest(), key)

	var env struct {
		Payload    string      `json:"payload"`
		Signatures []Signature `json:"signatures"`
	}
	if err := json.Unmarshal(valid, &env); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"envelope unknown":   append(valid[:len(valid)-1], []byte(`,"unknown":true}`)...),
		"envelope trailing":  append(append([]byte(nil), valid...), []byte(` {}`)...),
		"envelope duplicate": []byte(`{"payload":"` + env.Payload + `","payload":"` + env.Payload + `","signatures":[]}`),
		"payload unknown":    envelopeBytes(t, append(payload[:len(payload)-1], []byte(`,"unknown":true}`)...), env.Signatures),
		"payload trailing":   envelopeBytes(t, append(append([]byte(nil), payload...), []byte(` {}`)...), env.Signatures),
		"payload duplicate":  envelopeBytes(t, duplicateFirstObjectField(t, payload), env.Signatures),
		"payload whitespace": envelopeBytes(t, append([]byte(" "), payload...), env.Signatures),
	}
	policy := policyFor(key)
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAndVerify(data, policy); !errors.Is(err, ErrMalformed) {
				t.Fatalf("got %v, want ErrMalformed", err)
			}
		})
	}
}

func envelopeBytes(t *testing.T, payload []byte, signatures []Signature) []byte {
	t.Helper()
	data, err := json.Marshal(struct {
		Payload    string      `json:"payload"`
		Signatures []Signature `json:"signatures"`
	}{base64.StdEncoding.EncodeToString(payload), signatures})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func duplicateFirstObjectField(t *testing.T, payload []byte) []byte {
	t.Helper()
	if !strings.HasPrefix(string(payload), `{"schema":`) {
		t.Fatalf("unexpected payload: %s", payload)
	}
	return []byte(strings.Replace(string(payload), `{"schema":`, `{"schema":"update-manifest-v2","schema":`, 1))
}

func TestDomainSeparatedSignatureRejectsOrdinaryPayloadDigest(t *testing.T) {
	key := newKey(t)
	manifest := validManifest()
	payload, err := CanonicalPayload(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Assemble(manifest, []DetachedSignature{{KeyID: "key-a", Value: sig}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAndVerify(encoded, policyFor(key)); !errors.Is(err, ErrInsufficientSignatures) {
		t.Fatalf("got %v, want domain-separated signature rejection", err)
	}
}

func TestCanonicalPayloadRoundTripAndArtifactOrdering(t *testing.T) {
	manifest := validManifest()
	manifest.Artifacts[0], manifest.Artifacts[1] = manifest.Artifacts[1], manifest.Artifacts[0]
	payload, err := CanonicalPayload(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePayload(payload)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}
	if parsed.Artifacts[0].OS != "darwin" || parsed.Artifacts[1].OS != "linux" {
		t.Fatalf("artifacts are not canonical: %+v", parsed.Artifacts)
	}
	if _, err := ParsePayload(append([]byte(" "), payload...)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("noncanonical payload got %v", err)
	}
}

func TestCanonicalPayloadDoesNotMutateCaller(t *testing.T) {
	manifest := validManifest()
	first := manifest.Artifacts[0]
	if _, err := CanonicalPayload(manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Artifacts[0] != first {
		t.Fatal("CanonicalPayload mutated caller artifact order")
	}
}

func FuzzParsePayloadNeverPanics(f *testing.F) {
	canonical, err := CanonicalPayload(validManifest())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(canonical)
	f.Add([]byte(`{"schema":null}`))
	f.Add([]byte{0xff, 0x00, '{', '}'})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = ParsePayload(payload)
	})
}

func TestSelectArtifactExactPlatform(t *testing.T) {
	manifest := validManifest()
	artifact, err := SelectArtifact(manifest, "darwin", "arm64")
	if err != nil || artifact.URL == "" {
		t.Fatalf("SelectArtifact: %+v, %v", artifact, err)
	}
	if _, err := SelectArtifact(manifest, "linux", "arm64"); !errors.Is(err, ErrNoArtifact) {
		t.Fatalf("got %v, want ErrNoArtifact", err)
	}

	manifest.Artifacts = append(manifest.Artifacts, manifest.Artifacts[0])
	if _, err := CanonicalPayload(manifest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("duplicate platform got %v", err)
	}
}
