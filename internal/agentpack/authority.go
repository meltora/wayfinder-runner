package agentpack

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

// PublicationAttestationDigest reproduces the control plane's recursively
// key-sorted JSON checksum. The signed message is the raw 32-byte digest, not
// its hexadecimal representation.
func PublicationAttestationDigest(authority v1.SignedAgentPackAuthority) (string, error) {
	canonical, err := publicationAttestationJSON(authority)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyDeliveryAuthority verifies the complete signed publication before any
// network request or archive processing is allowed.
func VerifyDeliveryAuthority(
	authority v1.SignedAgentPackAuthority,
	reference v1.AgentPackRef,
	publicKeySPKIBase64 string,
	runnerProvider string,
	runnerVersion string,
) error {
	if err := validation.SignedAgentPackAuthority(authority); err != nil {
		return fmt.Errorf("invalid signed Agent Pack authority: %w", err)
	}
	if err := validation.AgentPackReference(reference); err != nil {
		return fmt.Errorf("invalid Agent Pack reference: %w", err)
	}
	if !validation.AgentPackAuthorityBindsReference(authority, reference) {
		return fmt.Errorf("signed Agent Pack authority does not bind the requested archive")
	}
	if canonicalProvider(authority.Provider) != canonicalProvider(runnerProvider) {
		return fmt.Errorf("signed Agent Pack provider %q does not match runner provider", authority.Provider)
	}
	compatible, err := runnerMeetsMinimum(runnerVersion, authority.MinimumRunnerVersion)
	if err != nil {
		return fmt.Errorf("compare runner version: %w", err)
	}
	if !compatible {
		return fmt.Errorf("runner %s is below Agent Pack minimum %s", runnerVersion, authority.MinimumRunnerVersion)
	}

	digest, err := PublicationAttestationDigest(authority)
	if err != nil {
		return err
	}
	if digest != authority.AttestationDigest {
		return fmt.Errorf("Agent Pack attestation digest mismatch")
	}
	publicKey, err := ed25519PublicKeyFromSPKIBase64(publicKeySPKIBase64)
	if err != nil {
		return fmt.Errorf("trusted Agent Pack key: %w", err)
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(authority.SignatureBase64)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("Agent Pack attestation has an invalid signature encoding")
	}
	digestBytes, err := hex.DecodeString(digest)
	if err != nil {
		return fmt.Errorf("decode Agent Pack attestation digest: %w", err)
	}
	if !ed25519.Verify(publicKey, digestBytes, signature) {
		return fmt.Errorf("Agent Pack attestation signature verification failed")
	}
	return nil
}

func ed25519PublicKeyFromSPKIBase64(encoded string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(der) != encoded {
		return nil, fmt.Errorf("invalid canonical base64")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("invalid PKIX public key")
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must use Ed25519")
	}
	return publicKey, nil
}

func canonicalProvider(value string) string {
	switch value {
	case "claude-code":
		return "claude"
	default:
		return value
	}
}

func runnerMeetsMinimum(current, minimum string) (bool, error) {
	currentParts, err := semanticVersionParts(current)
	if err != nil {
		return false, fmt.Errorf("invalid runner version %q", current)
	}
	minimumParts, err := semanticVersionParts(minimum)
	if err != nil {
		return false, fmt.Errorf("invalid minimum runner version %q", minimum)
	}
	for index := range currentParts {
		if currentParts[index] != minimumParts[index] {
			return currentParts[index] > minimumParts[index], nil
		}
	}
	return true, nil
}

func semanticVersionParts(value string) ([3]int64, error) {
	var out [3]int64
	parts := strings.Split(value, ".")
	if len(parts) != len(out) {
		return out, fmt.Errorf("not a stable semantic version")
	}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return out, fmt.Errorf("not a canonical stable semantic version")
		}
		component, err := strconv.ParseInt(part, 10, 32)
		if err != nil || component < 0 {
			return out, fmt.Errorf("invalid semantic version component")
		}
		out[index] = component
	}
	return out, nil
}

func publicationAttestationJSON(authority v1.SignedAgentPackAuthority) ([]byte, error) {
	values := []string{
		authority.AgentPack.Digest, authority.AgentPack.KeyID, authority.AgentPack.URL,
		authority.AgentPack.Version, authority.MinimumRunnerVersion, authority.PackKey,
		authority.PackName, authority.Provider, authority.Scope.Kind, authority.Scope.OrganizationID,
	}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("Agent Pack attestation contains invalid UTF-8")
		}
	}

	var out bytes.Buffer
	out.WriteString(`{"agentPack":{"digest":`)
	appendJSONString(&out, authority.AgentPack.Digest)
	out.WriteString(`,"key_id":`)
	appendJSONString(&out, authority.AgentPack.KeyID)
	out.WriteString(`,"url":`)
	appendJSONString(&out, authority.AgentPack.URL)
	out.WriteString(`,"version":`)
	appendJSONString(&out, authority.AgentPack.Version)
	out.WriteString(`},"minimumRunnerVersion":`)
	appendJSONString(&out, authority.MinimumRunnerVersion)
	out.WriteString(`,"packKey":`)
	appendJSONString(&out, authority.PackKey)
	out.WriteString(`,"packName":`)
	appendJSONString(&out, authority.PackName)
	out.WriteString(`,"protocolVersion":`)
	out.WriteString(strconv.Itoa(authority.ProtocolVersion))
	out.WriteString(`,"provider":`)
	appendJSONString(&out, authority.Provider)
	out.WriteString(`,"scope":{"kind":`)
	appendJSONString(&out, authority.Scope.Kind)
	if authority.Scope.Kind == v1.AgentPackScopeOrganization {
		out.WriteString(`,"organizationId":`)
		appendJSONString(&out, authority.Scope.OrganizationID)
	}
	out.WriteString(`}}`)
	return out.Bytes(), nil
}

func appendJSONString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				_, _ = fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}
