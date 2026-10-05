package agentpack

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var signingKeyProofNow = time.Date(2026, 10, 1, 10, 1, 0, 0, time.UTC)

func signingKeyProofFixture(spki string) SigningKeyProofMessage {
	organization := "org-a"
	return SigningKeyProofMessage{
		ActorUserID: "user-a", ChallengeID: "privileged_reauth_" + strings.Repeat("a", 64),
		ExpiresAt: "2026-10-01T10:05:00.000Z", KeyID: "release-2026", Nonce: strings.Repeat("b", 64),
		NotAfter: "2027-10-01T10:00:00.000Z", NotBefore: "2026-10-01T10:00:00.000Z",
		OrganizationID: &organization, Purpose: SigningKeyProofPurpose, ScopeType: "ORGANIZATION", SPKISHA256: spki,
	}
}

func signingKeyProofSigner(t *testing.T) (*FileSigner, PublicKeyIdentity) {
	t.Helper()
	path, _, _ := writeEd25519KeyFile(t, t.TempDir())
	signer, err := NewFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := SignerPublicKeyIdentity(signer)
	if err != nil {
		t.Fatal(err)
	}
	return signer, identity
}

func TestSigningKeyProofCanonicalJSONMatchesTheAP1Layout(t *testing.T) {
	message := signingKeyProofFixture(strings.Repeat("c", 64))
	want := `{"actorUserId":"user-a","challengeId":"privileged_reauth_` + strings.Repeat("a", 64) +
		`","expiresAt":"2026-10-01T10:05:00.000Z","keyId":"release-2026","nonce":"` + strings.Repeat("b", 64) +
		`","notAfter":"2027-10-01T10:00:00.000Z","notBefore":"2026-10-01T10:00:00.000Z","organizationId":"org-a",` +
		`"purpose":"shortstack.agent-pack.signing-key-registration.v1","scopeType":"ORGANIZATION","spkiSha256":"` + strings.Repeat("c", 64) + `"}`
	if got := string(message.CanonicalJSON()); got != want {
		t.Fatalf("canonical = %s\nwant %s", got, want)
	}
	message.ScopeType, message.OrganizationID = "PLATFORM", nil
	if !strings.Contains(string(message.CanonicalJSON()), `"organizationId":null,`) {
		t.Fatal("platform proof does not serialize a null organization")
	}
}

func TestSignSigningKeyProofSignsOnlyCanonicalProofMessagesForTheOwnKey(t *testing.T) {
	signer, identity := signingKeyProofSigner(t)
	valid := signingKeyProofFixture(identity.SPKISHA256)
	raw := valid.CanonicalJSON()
	signature, err := SignSigningKeyProof(context.Background(), signer, raw, signingKeyProofNow)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := ed25519PublicKeyFromSPKIBase64(identity.PublicKeySPKIBase64)
	if !ed25519.Verify(public, raw, signature) {
		t.Fatal("proof signature does not verify over the message bytes")
	}
	platform := valid
	platform.ScopeType, platform.OrganizationID = "PLATFORM", nil
	if _, err := SignSigningKeyProof(context.Background(), signer, platform.CanonicalJSON(), signingKeyProofNow); err != nil {
		t.Fatalf("platform proof refused: %v", err)
	}
	for _, id := range []string{"cjld2cjxh0000qzrmn831i7rn", "0b8a1f5e-3c2d-4e6f-8a9b-1c2d3e4f5a6b", "organization-1", "org.a_b-c", "A", strings.Repeat("a", 128)} {
		accepted := valid
		organization := id
		accepted.ActorUserID, accepted.OrganizationID = id, &organization
		if _, err := SignSigningKeyProof(context.Background(), signer, accepted.CanonicalJSON(), signingKeyProofNow); err != nil {
			t.Fatalf("proof with AP-1 id %q refused: %v", id, err)
		}
	}
	for _, id := range []string{"user~1", "~user", ".user", "-user", "_user", "user 1", strings.Repeat("a", 129)} {
		for field, edit := range map[string]func(*SigningKeyProofMessage, string){
			"actor":        func(m *SigningKeyProofMessage, value string) { m.ActorUserID = value },
			"organization": func(m *SigningKeyProofMessage, value string) { m.OrganizationID = &value },
		} {
			refused := signingKeyProofFixture(identity.SPKISHA256)
			edit(&refused, id)
			if signature, err := SignSigningKeyProof(context.Background(), signer, refused.CanonicalJSON(), signingKeyProofNow); err == nil || signature != nil {
				t.Fatalf("proof with %s id %q outside the AP-1 alphabet was signed", field, id)
			}
		}
	}

	canonical := string(raw)
	mutate := func(edit func(*SigningKeyProofMessage)) []byte {
		message := signingKeyProofFixture(identity.SPKISHA256)
		edit(&message)
		return message.CanonicalJSON()
	}
	invalidOrganization := "org b"
	refusals := map[string][]byte{
		"empty":                   nil,
		"arbitrary text":          []byte("please sign this"),
		"thirty-two bytes":        []byte(strings.Repeat("x", 32)),
		"json array":              []byte(`[` + canonical + `]`),
		"other purpose":           mutate(func(m *SigningKeyProofMessage) { m.Purpose = "shortstack.agent-pack.publication.v1" }),
		"other key":               mutate(func(m *SigningKeyProofMessage) { m.SPKISHA256 = strings.Repeat("d", 64) }),
		"expired":                 mutate(func(m *SigningKeyProofMessage) { m.ExpiresAt = "2026-10-01T10:01:00.000Z" }),
		"impossible date":         mutate(func(m *SigningKeyProofMessage) { m.ExpiresAt = "2026-02-30T10:05:00.000Z" }),
		"instant without millis":  mutate(func(m *SigningKeyProofMessage) { m.NotAfter = "2027-10-01T10:00:00Z" }),
		"window inverted":         mutate(func(m *SigningKeyProofMessage) { m.NotBefore = m.NotAfter }),
		"platform naming an org":  mutate(func(m *SigningKeyProofMessage) { m.ScopeType = "PLATFORM" }),
		"organization without id": mutate(func(m *SigningKeyProofMessage) { m.OrganizationID = nil }),
		"unknown scope":           mutate(func(m *SigningKeyProofMessage) { m.ScopeType = "GLOBAL" }),
		"bad challenge":           mutate(func(m *SigningKeyProofMessage) { m.ChallengeID = "challenge-1" }),
		"bad nonce":               mutate(func(m *SigningKeyProofMessage) { m.Nonce = strings.Repeat("B", 64) }),
		"bad actor":               mutate(func(m *SigningKeyProofMessage) { m.ActorUserID = "../user" }),
		"bad key id":              mutate(func(m *SigningKeyProofMessage) { m.KeyID = "" }),
		"tilde key id":            mutate(func(m *SigningKeyProofMessage) { m.KeyID = "release~2026" }),
		"leading tilde actor":     mutate(func(m *SigningKeyProofMessage) { m.ActorUserID = "~user" }),
		"long actor":              mutate(func(m *SigningKeyProofMessage) { m.ActorUserID = strings.Repeat("u", 129) }),
		"bad organization":        mutate(func(m *SigningKeyProofMessage) { m.OrganizationID = &invalidOrganization }),
		"whitespace":              []byte(strings.Replace(canonical, `,"keyId"`, `, "keyId"`, 1)),
		"trailing newline":        []byte(canonical + "\n"),
		"swapped keys":            swapProofKeys(canonical),
		"extra key":               []byte(strings.Replace(canonical, `{"actorUserId"`, `{"a":"b","actorUserId"`, 1)),
		"missing key":             []byte(strings.Replace(canonical, `"keyId":"release-2026",`, ``, 1)),
		"duplicate key":           []byte(strings.Replace(canonical, `"keyId":"release-2026",`, `"keyId":"release-2026","keyId":"release-2026",`, 1)),
		"escaped character":       []byte(strings.Replace(canonical, `"user-a"`, `"user`+"\\"+`u002da"`, 1)),
		"number value":            []byte(strings.Replace(canonical, `"user-a"`, `7`, 1)),
		"null actor":              []byte(strings.Replace(canonical, `"user-a"`, `null`, 1)),
		"invalid utf-8":           []byte(strings.Replace(canonical, "user-a", "user-\xff", 1)),
		"oversize":                []byte(canonical + strings.Repeat(" ", SigningKeyProofMaxBytes)),
	}
	for name, message := range refusals {
		t.Run(name, func(t *testing.T) {
			signature, err := SignSigningKeyProof(context.Background(), signer, message, signingKeyProofNow)
			if err == nil || signature != nil {
				t.Fatal("non-canonical proof message was signed")
			}
		})
	}
}

func swapProofKeys(canonical string) []byte {
	return []byte(strings.Replace(canonical, `"keyId":"release-2026","nonce":"`+strings.Repeat("b", 64)+`"`,
		`"nonce":"`+strings.Repeat("b", 64)+`","keyId":"release-2026"`, 1))
}

func TestSignSigningKeyProofRefusesPublicationAttestationForgery(t *testing.T) {
	signer, identity := signingKeyProofSigner(t)
	forged := v1.SignedAgentPackAuthority{
		SchemaVersion: v1.AgentPackAuthoritySchemaVersion, RegistryVersionID: "unpublished",
		Scope: v1.AgentPackAuthorityScope{Kind: v1.AgentPackScopePlatform}, PackKey: "attacker-pack", PackName: "Attacker",
		Provider: "claude-code", ProtocolVersion: v1.ProtocolVersion, MinimumRunnerVersion: "0.1.0",
		AgentPack: v1.AgentPackRef{Version: "1.0.0", Digest: strings.Repeat("f", 64), URL: "https://evil.example/pack.tgz", KeyID: "platform-2026"},
	}
	digest, err := PublicationAttestationDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(digest)
	if signature, err := SignSigningKeyProof(context.Background(), signer, raw, signingKeyProofNow); err == nil || signature != nil {
		t.Fatal("prove signed a publication attestation digest")
	}
	attestationJSON, err := publicationAttestationJSON(forged)
	if err != nil {
		t.Fatal(err)
	}
	if signature, err := SignSigningKeyProof(context.Background(), signer, attestationJSON, signingKeyProofNow); err == nil || signature != nil {
		t.Fatal("prove signed a publication attestation document")
	}
	if shortest := len(signingKeyProofFixture(identity.SPKISHA256).CanonicalJSON()); shortest <= len(raw) {
		t.Fatalf("a proof message (%d bytes) can collide with a publication digest", shortest)
	}
}

func TestSignSigningKeyProofRequiresASigner(t *testing.T) {
	if _, err := SignSigningKeyProof(context.Background(), nil, []byte("{}"), signingKeyProofNow); err == nil {
		t.Fatal("nil signer accepted")
	}
}
