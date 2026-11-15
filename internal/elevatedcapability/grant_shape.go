package elevatedcapability

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var declarationDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func GrantShapeValid(grant v1.ElevatedCapabilityGrant) bool {
	if grant.ExpiresAt.IsZero() || !declarationDigestPattern.MatchString(grant.DeclarationDigest) || !validKeyText(grant.Key) {
		return false
	}
	if grant.AgentPackDigest != "" && (grant.RepositoryID != "" || !declarationDigestPattern.MatchString(grant.AgentPackDigest)) {
		return false
	}
	switch grant.Kind {
	case v1.ElevatedCapabilityNetworkAccess:
		return grant.AgentPackDigest == ""
	case v1.ElevatedCapabilityHook, v1.ElevatedCapabilityMCPServer:
		return ValidRepositoryKey(grant.Key)
	default:
		return false
	}
}

func ValidRepositoryKey(key string) bool {
	if strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validKeyText(value string) bool {
	if value == "" || len(value) > v1.MaxElevatedCapabilityKeyBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
