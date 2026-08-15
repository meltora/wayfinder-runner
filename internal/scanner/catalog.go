package scanner

import (
	"path"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type catalogMatch struct {
	sourceType v1.AgentConfigSourceType
	provider   string
}

var developmentInstructionNames = map[string]bool{
	"BUILD.md": true, "CONTRIBUTING.md": true, "DEVELOPMENT.md": true,
	"MIGRATIONS.md": true, "RUNBOOK.md": true, "TESTING.md": true,
}

func hasDirectorySequence(parts []string, sequence ...string) bool {
	for start := 0; start+len(sequence) < len(parts); start++ {
		matched := true
		for offset := range sequence {
			if parts[start+offset] != sequence[offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func matchCatalog(relativePath string) (catalogMatch, bool) {
	parts := strings.Split(relativePath, "/")
	base := path.Base(relativePath)
	lowerBase := strings.ToLower(base)

	if isCredentialName(lowerBase, parts) {
		return catalogMatch{sourceType: v1.AgentConfigCredential}, true
	}
	if isAuthCacheName(lowerBase, parts) {
		return catalogMatch{sourceType: v1.AgentConfigAuthCache}, true
	}
	if base == "AGENTS.md" {
		return catalogMatch{sourceType: v1.AgentConfigRepositoryInstructions, provider: "codex"}, true
	}
	if base == "CLAUDE.md" {
		return catalogMatch{sourceType: v1.AgentConfigRepositoryInstructions, provider: "claude"}, true
	}
	for _, provider := range []string{"claude", "codex", "opencode"} {
		if hasDirectorySequence(parts, "."+provider, "skills") {
			return catalogMatch{sourceType: v1.AgentConfigSkill, provider: provider}, true
		}
	}
	if hasDirectorySequence(parts, ".agents", "skills") {
		return catalogMatch{sourceType: v1.AgentConfigSkill}, true
	}
	for _, provider := range []string{"claude", "opencode"} {
		if hasDirectorySequence(parts, "."+provider, "commands") {
			return catalogMatch{sourceType: v1.AgentConfigCommand, provider: provider}, true
		}
		if hasDirectorySequence(parts, "."+provider, "agents") {
			return catalogMatch{sourceType: v1.AgentConfigAgentDefinition, provider: provider}, true
		}
	}
	if lowerBase == ".mcp.json" {
		return catalogMatch{sourceType: v1.AgentConfigMCPDeclaration}, true
	}
	if hasDirectorySequence(parts, ".claude", "hooks") {
		return catalogMatch{sourceType: v1.AgentConfigHook, provider: "claude"}, true
	}
	if hasDirectorySequence(parts, ".yourplatform", "hooks") {
		return catalogMatch{sourceType: v1.AgentConfigHook}, true
	}
	if hasDirectorySequence(parts, ".opencode", "plugins") || hasDirectorySequence(parts, ".opencode", "tools") {
		return catalogMatch{sourceType: v1.AgentConfigExecutableExtension, provider: "opencode"}, true
	}
	if hasDirectorySequence(parts, ".claude", "plugins") {
		return catalogMatch{sourceType: v1.AgentConfigExecutableExtension, provider: "claude"}, true
	}
	if isProviderConfiguration(parts, lowerBase) {
		return catalogMatch{sourceType: v1.AgentConfigProviderConfiguration, provider: providerForPath(parts, lowerBase)}, true
	}
	if hasDirectorySequence(parts, ".yourplatform") &&
		(strings.HasPrefix(lowerBase, "manifest.") || strings.HasPrefix(lowerBase, "config.")) {
		return catalogMatch{sourceType: v1.AgentConfigPlatformManifest}, true
	}
	if developmentInstructionNames[base] {
		return catalogMatch{sourceType: v1.AgentConfigDevelopmentInstruction}, true
	}
	return catalogMatch{}, false
}

func matchCatalogContainer(relativePath string) (catalogMatch, bool) {
	parts := strings.Split(relativePath, "/")
	for _, part := range parts {
		switch part {
		case ".claude":
			return catalogMatch{sourceType: v1.AgentConfigSymlink, provider: "claude"}, true
		case ".codex":
			return catalogMatch{sourceType: v1.AgentConfigSymlink, provider: "codex"}, true
		case ".opencode":
			return catalogMatch{sourceType: v1.AgentConfigSymlink, provider: "opencode"}, true
		case ".agents", ".yourplatform":
			return catalogMatch{sourceType: v1.AgentConfigSymlink}, true
		}
	}
	return catalogMatch{}, false
}

func isProviderConfiguration(parts []string, lowerBase string) bool {
	if lowerBase == "opencode.json" || lowerBase == "opencode.jsonc" || lowerBase == "codex.toml" {
		return true
	}
	if hasDirectorySequence(parts, ".codex") && lowerBase == "config.toml" {
		return true
	}
	if hasDirectorySequence(parts, ".claude") && (lowerBase == "settings.json" || lowerBase == "settings.local.json") {
		return true
	}
	return hasDirectorySequence(parts, ".opencode") && (lowerBase == "config.json" || lowerBase == "config.jsonc")
}

func providerForPath(parts []string, lowerBase string) string {
	joined := "/" + strings.Join(parts, "/") + "/"
	switch {
	case strings.Contains(joined, "/.claude/"):
		return "claude"
	case strings.Contains(joined, "/.codex/") || lowerBase == "codex.toml":
		return "codex"
	case strings.Contains(joined, "/.opencode/") || strings.HasPrefix(lowerBase, "opencode."):
		return "opencode"
	default:
		return ""
	}
}
