package validation

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var (
	packKeyPattern       = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

func AgentPackReference(reference v1.AgentPackRef) error {
	if err := ID("agent_pack.key_id", reference.KeyID); err != nil {
		return err
	}
	if err := boundedText("agent_pack.version", reference.Version, 128, true); err != nil {
		return err
	}
	if err := SHA256Digest("agent_pack.digest", reference.Digest); err != nil {
		return err
	}
	if err := strictHTTPSURL("agent_pack.url", reference.URL); err != nil {
		return err
	}
	return nil
}

func SignedAgentPackAuthority(authority v1.SignedAgentPackAuthority) error {
	if authority.SchemaVersion != v1.AgentPackAuthoritySchemaVersion {
		return fmt.Errorf("agent_pack_authority.schema_version must be %d", v1.AgentPackAuthoritySchemaVersion)
	}
	if err := ID("agent_pack_authority.registry_version_id", authority.RegistryVersionID); err != nil {
		return err
	}
	switch authority.Scope.Kind {
	case v1.AgentPackScopePlatform:
		if authority.Scope.OrganizationID != "" {
			return fmt.Errorf("platform Agent Pack scope cannot contain organization_id")
		}
	case v1.AgentPackScopeOrganization:
		if err := ID("agent_pack_authority.scope.organization_id", authority.Scope.OrganizationID); err != nil {
			return err
		}
	default:
		return fmt.Errorf("agent_pack_authority.scope.kind is unsupported")
	}
	if len(authority.PackKey) == 0 || len(authority.PackKey) > 128 || !packKeyPattern.MatchString(authority.PackKey) {
		return fmt.Errorf("agent_pack_authority.pack_key must be a bounded lowercase kebab-case key")
	}
	if err := boundedText("agent_pack_authority.pack_name", authority.PackName, 256, true); err != nil {
		return err
	}
	if IsECMAScriptBlank(authority.PackName) {
		return fmt.Errorf("agent_pack_authority.pack_name must contain visible text")
	}
	if err := ID("agent_pack_authority.provider", authority.Provider); err != nil {
		return err
	}
	if authority.ProtocolVersion != v1.ProtocolVersion {
		return fmt.Errorf("agent_pack_authority.protocol_version must be %d", v1.ProtocolVersion)
	}
	if err := stableSemanticVersion(authority.MinimumRunnerVersion); err != nil {
		return fmt.Errorf("agent_pack_authority.minimum_runner_version: %w", err)
	}
	if err := AgentPackReference(authority.AgentPack); err != nil {
		return fmt.Errorf("agent_pack_authority: %w", err)
	}
	if err := SHA256Digest("agent_pack_authority.attestation_digest", authority.AttestationDigest); err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(authority.SignatureBase64)
	if err != nil || len(decoded) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(decoded) != authority.SignatureBase64 {
		return fmt.Errorf("agent_pack_authority.signature_base64 must be a canonical Ed25519 signature")
	}
	return nil
}

func AgentPackAuthorityBindsReference(authority v1.SignedAgentPackAuthority, reference v1.AgentPackRef) bool {
	bound := authority.AgentPack
	return bound.Version == reference.Version && bound.Digest == reference.Digest &&
		bound.URL == reference.URL && bound.KeyID == reference.KeyID
}

func strictHTTPSURL(field, raw string) error {
	if len(raw) == 0 || len(raw) > 4096 || !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("%s must be an absolute HTTPS URL without credentials or fragments", field)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute HTTPS URL without credentials or fragments", field)
	}
	if parsed.Opaque != "" || parsed.Hostname() == "" || strings.Contains(parsed.Host, "\\") {
		return fmt.Errorf("%s contains a non-canonical origin", field)
	}
	return nil
}

func stableSemanticVersion(value string) error {
	if len(value) == 0 || len(value) > 32 {
		return fmt.Errorf("must be a stable semantic version")
	}
	parts := stableVersionPattern.FindStringSubmatch(value)
	if len(parts) != 4 {
		return fmt.Errorf("must be a stable semantic version")
	}
	for _, part := range parts[1:] {
		component, err := strconv.ParseInt(part, 10, 32)
		if err != nil || component < 0 {
			return fmt.Errorf("semantic version components are too large")
		}
	}
	return nil
}
