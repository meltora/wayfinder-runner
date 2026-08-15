package scanner

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password)\s*[:=]\s*["']?[A-Za-z0-9_\-+/=]{16,}`),
	regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
}

var credentialNames = map[string]bool{
	".env": true, ".env.local": true, ".env.production": true,
	".npmrc": true, "credentials": true, ".credentials": true,
	"id_rsa": true, "id_ed25519": true,
}

var authCacheNames = map[string]bool{
	"auth.json": true, ".credentials.json": true, "cookies": true,
	"cookies.json": true, "session.json": true,
}

func isCredentialName(base string, parts []string) bool {
	if credentialNames[base] || strings.HasPrefix(base, ".env.") ||
		strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") {
		return true
	}
	for _, part := range parts[:len(parts)-1] {
		if part == ".ssh" || part == ".aws" {
			return true
		}
	}
	return false
}

func isAuthCacheName(base string, parts []string) bool {
	if authCacheNames[base] {
		return true
	}
	for _, part := range parts[:len(parts)-1] {
		if part == ".auth" || part == ".sessions" {
			return true
		}
	}
	return false
}

func containsSecret(data []byte) bool {
	for _, pattern := range secretPatterns {
		if pattern.Match(data) {
			return true
		}
	}
	return false
}

func isBinary(data []byte) bool {
	if !utf8.Valid(data) {
		return true
	}
	for _, value := range data {
		if value == 0 || (value < 0x20 && value != '\n' && value != '\r' && value != '\t') {
			return true
		}
	}
	return false
}
