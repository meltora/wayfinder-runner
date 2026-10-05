package capability

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (d *Detector) WithProviderEnvironment(environment map[string]map[string]string) *Detector {
	configured := *d
	configured.providerEnvironment = make(map[string][]string, 3)
	for _, name := range []string{"codex", "claude", "opencode"} {
		variables := environment[name]
		if len(variables) == 0 {
			continue
		}
		entries := make([]string, 0, len(variables))
		for key, value := range variables {
			entries = append(entries, key+"="+value)
		}
		sort.Strings(entries)
		configured.providerEnvironment[name] = entries
	}
	return &configured
}

func (d *Detector) runProviderVersion(ctx context.Context, name, binary string) (string, error) {
	env := d.providerEnvironment[name]
	if len(env) == 0 || d.RunProviderVersion == nil {
		return d.RunVersion(ctx, binary, "--version")
	}
	return d.RunProviderVersion(ctx, append([]string(nil), env...), binary, "--version")
}

func defaultRunProviderVersion(ctx context.Context, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// #nosec G204 -- callers use fixed tool names or validated operator-configured provider paths with --version and no shell.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (d *Detector) providerEnvironmentValues(name string) []string {
	values := make([]string, 0, len(d.providerEnvironment[name]))
	for _, entry := range d.providerEnvironment[name] {
		if _, value, ok := strings.Cut(entry, "="); ok && (len(value) >= 8 || strings.Contains(value, "/")) {
			values = append(values, value)
		}
	}
	return values
}

func (d *Detector) providerEnvironmentValue(provider, key string) string {
	value := ""
	for _, entry := range d.providerEnvironment[provider] {
		if name, candidate, ok := strings.Cut(entry, "="); ok && name == key {
			value = candidate
		}
	}
	return value
}

func (d *Detector) authReady(provider string) bool {
	switch provider {
	case "claude":
		if dir := d.providerEnvironmentValue(provider, "CLAUDE_CONFIG_DIR"); dir != "" {
			return fileExists(filepath.Join(dir, ".credentials.json")) || fileExists(filepath.Join(dir, ".claude.json"))
		}
	case "codex":
		if dir := d.providerEnvironmentValue(provider, "CODEX_HOME"); dir != "" {
			return fileExists(filepath.Join(dir, "auth.json"))
		}
	}
	return authReady(provider)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
