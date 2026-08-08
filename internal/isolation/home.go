package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/example/app-runner/internal/safeio"
)

func PrepareHome(stateDir, jobID, attemptID, provider string) (string, error) {
	homesRoot := filepath.Join(stateDir, "job-homes")
	if err := os.MkdirAll(homesRoot, 0o700); err != nil {
		return "", err
	}
	home, err := os.MkdirTemp(homesRoot, "home-*")
	if err != nil {
		return "", err
	}
	// #nosec G302 -- provider home is a private directory; owner traversal requires 0700.
	if err := os.Chmod(home, 0o700); err != nil {
		return "", err
	}
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var copies []struct{ src, dst string }
	switch provider {
	case "claude":
		copies = []struct{ src, dst string }{
			{filepath.Join(hostHome, ".claude", ".credentials.json"), filepath.Join(home, ".claude", ".credentials.json")},
			{filepath.Join(hostHome, ".claude.json"), filepath.Join(home, ".claude.json")},
		}
	case "codex":
		copies = []struct{ src, dst string }{
			{filepath.Join(hostHome, ".codex", "auth.json"), filepath.Join(home, ".codex", "auth.json")},
		}
	case "opencode":
		copies = []struct{ src, dst string }{
			{filepath.Join(hostHome, ".local", "share", "opencode", "auth.json"), filepath.Join(home, ".local", "share", "opencode", "auth.json")},
		}
	}
	copied := 0
	credCopied := false
	for _, c := range copies {
		data, err := readPrivateCredential(c.src)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(c.dst), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(c.dst, data, 0o600); err != nil {
			return "", err
		}
		copied++
		base := filepath.Base(c.dst)
		if base == ".credentials.json" || base == "auth.json" {
			credCopied = true
		}
	}
	if !credCopied && provider == "claude" && runtime.GOOS == "darwin" {
		data, err := keychainClaudeCredentials()
		if err == nil && len(data) > 0 {
			dst := filepath.Join(home, ".claude", ".credentials.json")
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return "", err
			}
			if err := os.WriteFile(dst, data, 0o600); err != nil {
				return "", err
			}
			copied++
		}
	}
	if copied == 0 {
		_ = os.RemoveAll(home)
		return "", fmt.Errorf("no %s credentials found under %s to copy", provider, hostHome)
	}
	if provider == "codex" {
		if err := writeCodexProfiles(home); err != nil {
			_ = os.RemoveAll(home)
			return "", err
		}
	}
	return home, nil
}

func writeCodexProfiles(home string) error {
	const profileTemplate = `default_permissions = "runner-job"
approval_policy = "never"

[permissions.runner-job]
description = "Runner-owned least-privilege job profile."
extends = %q

[permissions.runner-job.filesystem]
"/home/runner/.codex" = "deny"

[permissions.runner-job.network]
enabled = false
`
	codexRoot := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		return err
	}
	profiles := map[string]string{
		"runner-plan.config.toml":  fmt.Sprintf(profileTemplate, ":read-only"),
		"runner-build.config.toml": fmt.Sprintf(profileTemplate, ":workspace"),
	}
	for name, content := range profiles {
		if err := safeio.AtomicWriteFile(codexRoot, name, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write Codex profile %s: %w", name, err)
		}
	}
	return nil
}

func readPrivateCredential(path string) ([]byte, error) {
	return safeio.ReadRegularFile(path, 1<<20, 0o077)
}

func keychainClaudeCredentials() ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(out))), nil
}

func CleanupHome(home string) error {
	if home == "" {
		return nil
	}
	return os.RemoveAll(home)
}
