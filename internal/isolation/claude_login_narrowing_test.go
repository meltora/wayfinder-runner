package isolation

import (
	"path/filepath"
	"strings"
	"testing"
)

const hostCredentialsWithOtherTokens = `{"mcpOAuth":{"plugin:example|abc":{"accessToken":"mcp-secret","refreshToken":"mcp-refresh"}},"claudeAiOauth":{"accessToken":"host-login","expiresAt":1},"futureKey":{"token":"other-secret"}}`

const narrowedHostCredentials = `{"claudeAiOauth":{"accessToken":"host-login","expiresAt":1}}`

func assertNarrowedJobLogin(t *testing.T, home string) {
	t.Helper()
	got := readJobHomeFile(t, home, ".claude/.credentials.json")
	if got != narrowedHostCredentials {
		t.Fatalf("job credentials = %q, want only claudeAiOauth", got)
	}
	if strings.Contains(got, "mcp-secret") || strings.Contains(got, "other-secret") {
		t.Fatal("job credentials carry tokens other than the Claude login")
	}
}

func TestPrepareHomeCopiesOnlyTheClaudeLoginFromTheDefaultSource(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude", ".credentials.json"), hostCredentialsWithOtherTokens, 0o600)
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	assertNarrowedJobLogin(t, home)
	if got := readJobHomeFile(t, hostHome, ".claude/.credentials.json"); got != hostCredentialsWithOtherTokens {
		t.Fatal("host credentials were modified")
	}
}

func TestPrepareHomeCopiesOnlyTheClaudeLoginFromTheConfiguredDirectory(t *testing.T) {
	installHostClaudeLogin(t)
	configDir := filepath.Join(t.TempDir(), "claude-personal")
	writePrivateTestFile(t, filepath.Join(configDir, ".credentials.json"), hostCredentialsWithOtherTokens, 0o600)
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=" + configDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	assertNarrowedJobLogin(t, home)
}

func TestPrepareHomeCopiesOnlyTheClaudeLoginFromTheKeychain(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude.json"), `{"user":"host-default"}`, 0o600)
	stubKeychain(t, "darwin", hostCredentialsWithOtherTokens)
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	assertNarrowedJobLogin(t, home)
}

func TestPrepareHomeRejectsAConfiguredLoginWithoutClaudeOAuth(t *testing.T) {
	installHostClaudeLogin(t)
	configDir := filepath.Join(t.TempDir(), "claude-personal")
	writePrivateTestFile(t, filepath.Join(configDir, ".credentials.json"), `{"mcpOAuth":{"x":{"accessToken":"mcp-secret"}}}`, 0o600)
	_, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=" + configDir})
	if err == nil {
		t.Fatal("a login without claudeAiOauth was accepted")
	}
	if strings.Contains(err.Error(), "mcp-secret") {
		t.Fatalf("error leaks content: %v", err)
	}
}

func TestPrepareHomeSkipsADefaultLoginWithoutClaudeOAuth(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude", ".credentials.json"), `{"mcpOAuth":{"x":{"accessToken":"mcp-secret"}}}`, 0o600)
	stubKeychain(t, "linux", "")
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", nil)
	if err == nil {
		defer func() { _ = CleanupHome(home) }()
		t.Fatal("a home was prepared without any Claude login")
	}
}
