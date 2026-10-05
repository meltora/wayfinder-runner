package agentpack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
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

func TestVerifyDeliveryAuthorityReturnsTypedSafeClassification(t *testing.T) {
	t.Parallel()
	authority, trustedKey := signAuthority(t, goldenAuthority())

	invalid := authority
	invalid.AttestationDigest = strings.Repeat("b", 64)
	if err := VerifyDeliveryAuthority(invalid, invalid.AgentPack, trustedKey, "claude", "1.4.0"); err == nil {
		t.Fatal("invalid authority accepted")
	} else if kind, ok := DeliveryAuthorityFailureKindOf(err); !ok || kind != DeliveryAuthorityInvalid {
		t.Fatalf("invalid authority classification = %q %v: %v", kind, ok, err)
	}

	if err := VerifyDeliveryAuthority(authority, authority.AgentPack, trustedKey, "codex", "1.4.0"); err == nil {
		t.Fatal("incompatible provider accepted")
	} else if kind, ok := DeliveryAuthorityFailureKindOf(err); !ok || kind != DeliveryAuthorityIncompatible {
		t.Fatalf("incompatible authority classification = %q %v: %v", kind, ok, err)
	}
}

type attestationVectorInput struct {
	Scope struct {
		Kind           string `json:"kind"`
		OrganizationID string `json:"organizationId"`
	} `json:"scope"`
	PackKey              string          `json:"packKey"`
	PackName             string          `json:"packName"`
	Provider             string          `json:"provider"`
	ProtocolVersion      int             `json:"protocolVersion"`
	MinimumRunnerVersion string          `json:"minimumRunnerVersion"`
	AgentPack            v1.AgentPackRef `json:"agentPack"`
}

type attestationVectorFile struct {
	Format  int `json:"format"`
	Vectors []struct {
		Name           string                 `json:"name"`
		Input          attestationVectorInput `json:"input"`
		ExpectedDigest string                 `json:"expectedDigest"`
	} `json:"vectors"`
}

func TestPublicationAttestationDigestReproducesSharedVectorCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/attestation-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file attestationVectorFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		t.Fatal(err)
	}
	if file.Format != 1 || len(file.Vectors) < 12 {
		t.Fatalf("vector corpus has %d vectors", len(file.Vectors))
	}
	seen := map[string]bool{}
	for _, vector := range file.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			input := vector.Input
			authority := v1.SignedAgentPackAuthority{
				Scope:   v1.AgentPackAuthorityScope{Kind: input.Scope.Kind, OrganizationID: input.Scope.OrganizationID},
				PackKey: input.PackKey, PackName: input.PackName, Provider: input.Provider,
				ProtocolVersion: input.ProtocolVersion, MinimumRunnerVersion: input.MinimumRunnerVersion, AgentPack: input.AgentPack,
			}
			digest, err := PublicationAttestationDigest(authority)
			if err != nil {
				t.Fatal(err)
			}
			if digest != vector.ExpectedDigest {
				t.Fatalf("digest = %s, want %s", digest, vector.ExpectedDigest)
			}
		})
		if seen[vector.ExpectedDigest] {
			t.Fatalf("vector %s duplicates another digest", vector.Name)
		}
		seen[vector.ExpectedDigest] = true
	}
	if file.Vectors[0].ExpectedDigest != goldenAttestationDigest {
		t.Fatal("first vector must be the control-plane golden vector")
	}
}
