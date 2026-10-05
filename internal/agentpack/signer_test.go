package agentpack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func TestSignPublicationAttestationProducesVerifiableAuthority(t *testing.T) {
	path, _, _ := writeEd25519KeyFile(t, t.TempDir())
	signer, err := NewFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := SignerPublicKeyIdentity(signer)
	if err != nil {
		t.Fatal(err)
	}
	authority := goldenAuthority()
	authority.AttestationDigest = ""
	signed, err := SignPublicationAttestation(context.Background(), signer, authority)
	if err != nil {
		t.Fatal(err)
	}
	if signed.AttestationDigest != goldenAttestationDigest {
		t.Fatalf("digest = %s", signed.AttestationDigest)
	}
	if err := VerifyDeliveryAuthority(signed, signed.AgentPack, identity.PublicKeySPKIBase64, "claude", "1.4.0"); err != nil {
		t.Fatalf("signed authority rejected: %v", err)
	}
	if _, err := SignPublicationAttestation(context.Background(), nil, authority); err == nil {
		t.Fatal("nil signer accepted")
	}
	invalid := authority
	invalid.PackName = "\xff"
	if _, err := SignPublicationAttestation(context.Background(), signer, invalid); err == nil {
		t.Fatal("invalid UTF-8 signed")
	}
	if _, err := SignerPublicKeyIdentity(nil); err == nil {
		t.Fatal("nil signer identity accepted")
	}
}

func TestGatewayArchiveURLUsesTheContentAddressedTemplate(t *testing.T) {
	digest := strings.Repeat("c", 64)
	for _, origin := range []string{"https://gateway.example.test", "https://gateway.example.test/", "https://gateway.example.test:8443"} {
		got, err := GatewayArchiveURL(origin, digest)
		if err != nil {
			t.Fatalf("%s: %v", origin, err)
		}
		if !strings.HasSuffix(got, "/v1/agent-packs/sha256/"+digest) || !strings.HasPrefix(got, "https://gateway.example.test") {
			t.Fatalf("url = %s", got)
		}
	}
	for _, origin := range []string{"http://gateway.example.test", "https://user@gateway.example.test", "https://gateway.example.test/base", "https://gateway.example.test?x=1", "https://gateway.example.test#f", "https://", "gateway.example.test", "https://gateway.example.test?"} {
		if _, err := GatewayArchiveURL(origin, digest); err == nil {
			t.Errorf("origin %q accepted", origin)
		}
	}
	if _, err := GatewayArchiveURL("https://gateway.example.test", "ABC"); err == nil {
		t.Fatal("invalid digest accepted")
	}
}

func TestSignPublishRequestBindsTheValidatedArchive(t *testing.T) {
	path, _, _ := writeEd25519KeyFile(t, t.TempDir())
	signer, err := NewFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := SignerPublicKeyIdentity(signer)
	archiveBytes, _ := writeSampleArchive(t, sampleArchiveContent())
	archive, err := ParseCanonicalArchive(archiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := GatewayArchiveURL("https://gateway.example.test", archive.Digest)
	request := PublicationRequest{
		ScopeKind: "platform", PackKey: "fixture", PackName: "Fixture Pack", Provider: "claude-code",
		ProtocolVersion: 1, MinimumRunnerVersion: "0.1.0", KeyID: "platform-2026", URL: location, RequestID: "request-1", Archive: archive,
	}
	signed, err := SignPublishRequest(context.Background(), signer, request)
	if err != nil {
		t.Fatal(err)
	}
	if signed.AgentPack.Digest != archive.Digest || signed.AgentPack.Version != "1.0.0" || signed.Scope.OrganizationID != "" {
		t.Fatalf("signed = %+v", signed)
	}
	authority := v1.SignedAgentPackAuthority{
		SchemaVersion: 1, RegistryVersionID: "r", Scope: v1.AgentPackAuthorityScope{Kind: "platform"},
		PackKey: signed.PackKey, PackName: signed.PackName, Provider: signed.Provider, ProtocolVersion: signed.ProtocolVersion,
		MinimumRunnerVersion: signed.MinimumRunnerVersion, AgentPack: signed.AgentPack, SignatureBase64: signed.SignatureBase64,
	}
	authority.AttestationDigest, _ = PublicationAttestationDigest(authority)
	if err := VerifyDeliveryAuthority(authority, authority.AgentPack, identity.PublicKeySPKIBase64, "claude", "0.1.0"); err != nil {
		t.Fatalf("signed publish request does not verify: %v", err)
	}
	mutations := map[string]func(*PublicationRequest){
		"no archive":       func(r *PublicationRequest) { r.Archive = nil },
		"pack key":         func(r *PublicationRequest) { r.PackKey = "other" },
		"provider":         func(r *PublicationRequest) { r.Provider = "codex" },
		"request id":       func(r *PublicationRequest) { r.RequestID = "../x" },
		"scope":            func(r *PublicationRequest) { r.ScopeKind = "global" },
		"organization":     func(r *PublicationRequest) { r.ScopeKind = "organization" },
		"platform org":     func(r *PublicationRequest) { r.OrganizationID = "org-1" },
		"blank name":       func(r *PublicationRequest) { r.PackName = "   " },
		"long name":        func(r *PublicationRequest) { r.PackName = strings.Repeat("n", 257) },
		"invalid utf8":     func(r *PublicationRequest) { r.PackName = "\xed\xa0\x80" },
		"protocol":         func(r *PublicationRequest) { r.ProtocolVersion = 2 },
		"minimum version":  func(r *PublicationRequest) { r.MinimumRunnerVersion = "1.0" },
		"key id":           func(r *PublicationRequest) { r.KeyID = "" },
		"http url":         func(r *PublicationRequest) { r.URL = "http://gateway.example.test/x" },
		"bom name":         func(r *PublicationRequest) { r.PackName = "\ufeff" },
		"ideographic name": func(r *PublicationRequest) { r.PackName = "\u3000\u2028\u00a0" },
		"nul name":         func(r *PublicationRequest) { r.PackName = "a\x00b" },
		"query url":        func(r *PublicationRequest) { r.URL = "https://gateway.example.test/x?a=b" },
		"fragment url":     func(r *PublicationRequest) { r.URL = "https://gateway.example.test/x#a" },
		"userinfo url":     func(r *PublicationRequest) { r.URL = "https://u:p@gateway.example.test/x" },
		"numeric host":     func(r *PublicationRequest) { r.URL = "https://1.2.3.256/x" },
		"punycode host":    func(r *PublicationRequest) { r.URL = "https://xn--a.test/x" },
		"uppercase host":   func(r *PublicationRequest) { r.URL = "https://Gateway.example.test/x" },
		"port range":       func(r *PublicationRequest) { r.URL = "https://gateway.example.test:70000/x" },
		"bad escape":       func(r *PublicationRequest) { r.URL = "https://gateway.example.test/%zz" },
		"backslash":        func(r *PublicationRequest) { r.URL = "https://gateway.example.test\\x" },
		"long label":       func(r *PublicationRequest) { r.URL = "https://" + strings.Repeat("a", 64) + ".test/x" },
		"long url":         func(r *PublicationRequest) { r.URL = "https://gateway.example.test/" + strings.Repeat("a", 4096) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if _, err := SignPublishRequest(context.Background(), signer, candidate); err == nil {
				t.Fatal("invalid publish request signed")
			}
		})
	}
	accepted := map[string]func(*PublicationRequest){
		"inner whitespace name": func(r *PublicationRequest) { r.PackName = " Fixture\u3000Pack " },
		"next line name":        func(r *PublicationRequest) { r.PackName = "\u0085" },
		"port and escapes":      func(r *PublicationRequest) { r.URL = "https://cdn-1.example.test:8443/packs/%41b/a~b.tgz" },
		"single label host":     func(r *PublicationRequest) { r.URL = "https://localhost/x" },
	}
	for name, mutate := range accepted {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if _, err := SignPublishRequest(context.Background(), signer, candidate); err != nil {
				t.Fatalf("valid publish request refused: %v", err)
			}
		})
	}
}

func TestECMAScriptWhitespaceMatchesTheSharedVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ecmascript-whitespace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		CodePoints []rune `json:"codePoints"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil || len(vector.CodePoints) == 0 {
		t.Fatalf("vector err = %v", err)
	}
	expected := make(map[rune]bool, len(vector.CodePoints))
	for _, codePoint := range vector.CodePoints {
		expected[codePoint] = true
	}
	for character := rune(0); character <= unicode.MaxRune; character++ {
		if validation.IsECMAScriptWhitespace(character) != expected[character] {
			t.Fatalf("U+%04X: Go = %v, vector = %v", character, validation.IsECMAScriptWhitespace(character), expected[character])
		}
	}
	if !validation.IsECMAScriptBlank("") || !validation.IsECMAScriptBlank("\ufeff\t") || validation.IsECMAScriptBlank("\u0085") || validation.IsECMAScriptBlank("\u200b") {
		t.Fatal("blank classification differs from String.prototype.trim")
	}
}
