package validation

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var elevatedCapabilityKinds = map[v1.ElevatedCapabilityKind]bool{
	v1.ElevatedCapabilityHook:          true,
	v1.ElevatedCapabilityMCPServer:     true,
	v1.ElevatedCapabilityNetworkAccess: true,
}

func ElevatedCapabilities(value v1.ElevatedCapabilityGrants) error {
	if value.SchemaVersion != v1.ElevatedCapabilityTransportVersion {
		return fmt.Errorf("elevated_capabilities schema_version is unsupported")
	}
	if len(value.Grants) < 1 || len(value.Grants) > v1.MaxElevatedCapabilityGrants {
		return fmt.Errorf("elevated_capabilities grants are out of range")
	}
	seen := map[string]bool{}
	for index, grant := range value.Grants {
		field := fmt.Sprintf("elevated_capabilities.grants[%d]", index)
		if err := ID(field+".request_id", grant.RequestID); err != nil {
			return err
		}
		if seen[grant.RequestID] {
			return fmt.Errorf("%s.request_id appears more than once", field)
		}
		seen[grant.RequestID] = true
		if !elevatedCapabilityKinds[grant.Kind] {
			return fmt.Errorf("%s.kind is unsupported", field)
		}
		if err := elevatedCapabilityKey(field+".key", grant.Key); err != nil {
			return err
		}
		if err := SHA256Digest(field+".declaration_digest", grant.DeclarationDigest); err != nil {
			return err
		}
		if grant.ExpiresAt.IsZero() {
			return fmt.Errorf("%s.expires_at is required", field)
		}
	}
	return nil
}

func elevatedCapabilityKey(field, value string) error {
	if value == "" || len(value) > v1.MaxElevatedCapabilityKeyBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains control characters", field)
		}
	}
	return nil
}
