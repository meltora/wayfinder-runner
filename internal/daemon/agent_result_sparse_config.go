package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
)

const maxAgentConfigBytes = 1 << 20

type configBool int

const (
	configUnset configBool = iota
	configFalse
	configTrue
	configUnknown
)

func sparseCheckoutEnabled(workspace *os.Root, commonGitRel, adminRel string) bool {
	common, ok := readAgentConfig(workspace, path.Join(commonGitRel, "config"))
	if !ok {
		return true
	}
	value := configValue(common, "core", "sparsecheckout")
	if configValue(common, "extensions", "worktreeconfig") == configTrue {
		worktree, ok := readAgentConfig(workspace, path.Join(adminRel, "config.worktree"))
		if !ok {
			return true
		}
		if override := configValue(worktree, "core", "sparsecheckout"); override != configUnset {
			value = override
		}
	}
	return value == configTrue || value == configUnknown
}

func readAgentConfig(workspace *os.Root, rel string) (string, bool) {
	content, err := readWorkspaceFile(workspace, rel, maxAgentConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return "", true
	}
	return string(content), err == nil
}

func configValue(content, section, key string) configBool {
	result := configUnset
	current := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			header, rest, closed := strings.Cut(line[1:], "]")
			if !closed {
				current = ""
				continue
			}
			current = configSectionName(header)
			line = strings.TrimSpace(rest)
		}
		if current != section || line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		name, value, hasValue := strings.Cut(line, "=")
		if !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		if !hasValue {
			result = configTrue
			continue
		}
		result = parseConfigBool(configScalar(value))
	}
	return result
}

func configSectionName(header string) string {
	header = strings.TrimSpace(header)
	if strings.ContainsAny(header, " \t\"") {
		return "\x00subsection"
	}
	return strings.ToLower(header)
}

func configScalar(value string) string {
	var builder strings.Builder
	quoted := false
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r == '"':
			quoted = !quoted
		case !quoted && (r == '#' || r == ';'):
			return strings.TrimSpace(builder.String())
		default:
			builder.WriteRune(r)
		}
	}
	return strings.TrimSpace(builder.String())
}

func parseConfigBool(value string) configBool {
	switch strings.ToLower(value) {
	case "true", "yes", "on", "1":
		return configTrue
	case "false", "no", "off", "0", "":
		return configFalse
	}
	return configUnknown
}
