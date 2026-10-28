package provider

import (
	"regexp"
	"sort"
	"strings"
)

type CLIOptionContract interface {
	RequiredCLIOptions() []string
}

var claudeRequiredCLIOptions = []string{
	"--add-dir", "--allowedTools", "--bare", "--disallowedTools", "--input-format", "--json-schema",
	"--mcp-config", "--model", "--no-session-persistence", "--output-format", "--permission-mode",
	"--permission-prompts", "--plugin-dir", "--print", "--restricted", "--resume", "--setting-sources",
	"--settings", "--strict-mcp-config", "--tools", "--verbose",
}

func (c *Claude) RequiredCLIOptions() []string {
	return append([]string(nil), claudeRequiredCLIOptions...)
}

var longOptionName = regexp.MustCompile(`^--[A-Za-z0-9][A-Za-z0-9-]*$`)

func DeclaredCLIOptions(help string) map[string]struct{} {
	declared := map[string]struct{}{}
	for _, line := range strings.Split(help, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "  -") {
			continue
		}
		head := line[2:]
		if end := strings.Index(head, "  "); end >= 0 {
			head = head[:end]
		}
		for _, part := range strings.Split(head, ", ") {
			if end := strings.IndexAny(part, " ="); end >= 0 {
				part = part[:end]
			}
			if longOptionName.MatchString(part) {
				declared[part] = struct{}{}
			}
		}
	}
	return declared
}

func MissingCLIOptions(help string, required []string) []string {
	declared := DeclaredCLIOptions(help)
	seen := map[string]struct{}{}
	var missing []string
	for _, option := range required {
		if _, ok := declared[option]; ok {
			continue
		}
		if _, ok := seen[option]; ok {
			continue
		}
		seen[option] = struct{}{}
		missing = append(missing, option)
	}
	sort.Strings(missing)
	return missing
}
