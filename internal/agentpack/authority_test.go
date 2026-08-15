package agentpack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const goldenAttestationDigest = "0dd810eda00dbaea28d88b70f32f88847ffb22481e07c6c0c12e6b87a8fec978"

func goldenAuthority() v1.SignedAgentPackAuthority {
	reference := v1.AgentPackRef{
		Version: "2.1.0", Digest: strings.Repeat("a", 64),
		URL: "https://packs.example.test/platform-default-2.1.0.tgz", KeyID: "pack-signing-1",
	}
	return v1.SignedAgentPackAuthority{
		SchemaVersion: 1, RegistryVersionID: "agent-pack-version-1",
		Scope:   v1.AgentPackAuthorityScope{Kind: "organization", OrganizationID: "org-1"},
		PackKey: "platform-default", PackName: "Platform Default", Provider: "claude-code",
		ProtocolVersion: 1, MinimumRunnerVersion: "1.4.0", AgentPack: reference,
		AttestationDigest: goldenAttestationDigest,
	}
}

func signAuthority(t *testing.T, authority v1.SignedAgentPackAuthority) (v1.SignedAgentPackAuthority, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hex.DecodeString(authority.AttestationDigest)
	if err != nil {
		t.Fatal(err)
	}
	authority.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, digest))
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return authority, base64.StdEncoding.EncodeToString(der)
}

func TestPublicationAttestationDigestMatchesControlPlaneGoldenVector(t *testing.T) {
	digest, err := PublicationAttestationDigest(goldenAuthority())
	if err != nil {
		t.Fatal(err)
	}
	if digest != goldenAttestationDigest {
		t.Fatalf("digest = %s, want control-plane vector %s", digest, goldenAttestationDigest)
	}
}

func TestVerifyDeliveryAuthorityChecksSignatureProviderVersionAndBinding(t *testing.T) {
	authority, trustedKey := signAuthority(t, goldenAuthority())
	if err := VerifyDeliveryAuthority(authority, authority.AgentPack, trustedKey, "claude", "1.4.0"); err != nil {
		t.Fatalf("valid authority rejected: %v", err)
	}

	tests := map[string]func(*v1.SignedAgentPackAuthority, *v1.AgentPackRef, *string, *string, *string){
		"digest mismatch": func(a *v1.SignedAgentPackAuthority, _ *v1.AgentPackRef, _ *string, _, _ *string) {
			a.AttestationDigest = strings.Repeat("b", 64)
		},
		"signature mismatch": func(a *v1.SignedAgentPackAuthority, _ *v1.AgentPackRef, _ *string, _, _ *string) {
			raw, _ := base64.StdEncoding.DecodeString(a.SignatureBase64)
			raw[0] ^= 0xff
			a.SignatureBase64 = base64.StdEncoding.EncodeToString(raw)
		},
		"wrong key": func(_ *v1.SignedAgentPackAuthority, _ *v1.AgentPackRef, key *string, _, _ *string) {
			_, replacement := signAuthority(t, goldenAuthority())
			*key = replacement
		},
		"ref mismatch": func(_ *v1.SignedAgentPackAuthority, ref *v1.AgentPackRef, _ *string, _, _ *string) {
			ref.Digest = strings.Repeat("c", 64)
		},
		"provider mismatch": func(_ *v1.SignedAgentPackAuthority, _ *v1.AgentPackRef, _ *string, provider, _ *string) {
			*provider = "codex"
		},
		"runner too old": func(_ *v1.SignedAgentPackAuthority, _ *v1.AgentPackRef, _ *string, _, version *string) {
			*version = "1.3.9"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := authority
			reference := authority.AgentPack
			key, provider, runnerVersion := trustedKey, "claude", "1.4.0"
			mutate(&candidate, &reference, &key, &provider, &runnerVersion)
			if err := VerifyDeliveryAuthority(candidate, reference, key, provider, runnerVersion); err == nil {
				t.Fatal("invalid delivery authority accepted")
			}
		})
	}
}
