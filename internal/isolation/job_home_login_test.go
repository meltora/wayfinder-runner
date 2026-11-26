package isolation

import (
	"crypto/sha256"
	"path/filepath"
	"testing"
)

const refreshableHostLogin = `{"mcpOAuth":{},"claudeAiOauth":{"accessToken":"a","refreshToken":"fake-refresh","expiresAt":1795713992053}}`

func TestPrepareHomeWithLoginRecordsTheConfiguredSource(t *testing.T) {
	installHostClaudeLogin(t)
	configDir := filepath.Join(t.TempDir(), "claude-personal")
	writePrivateTestFile(t, filepath.Join(configDir, ".credentials.json"), refreshableHostLogin, 0o600)
	jobHome, err := PrepareHomeWithLogin(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=" + configDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(jobHome.Path) }()
	if !jobHome.SyncsLogin || jobHome.ClaudeConfigDir != configDir || jobHome.ClaudeStateFile != filepath.Join(configDir, ".claude.json") {
		t.Fatalf("job home = %+v", jobHome)
	}
	if jobHome.Baseline != sha256.Sum256([]byte("fake-refresh")) {
		t.Fatal("baseline is not the copied refresh token's hash")
	}
}

func TestPrepareHomeWithLoginRecordsTheDefaultSource(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude", ".credentials.json"), refreshableHostLogin, 0o600)
	jobHome, err := PrepareHomeWithLogin(t.TempDir(), "j1", "a1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(jobHome.Path) }()
	if !jobHome.SyncsLogin || jobHome.ClaudeConfigDir != filepath.Join(hostHome, ".claude") || jobHome.ClaudeStateFile != filepath.Join(hostHome, ".claude.json") {
		t.Fatalf("job home = %+v", jobHome)
	}
}

func TestPrepareHomeWithLoginDoesNotSyncWithoutARefreshToken(t *testing.T) {
	installHostClaudeLogin(t)
	jobHome, err := PrepareHomeWithLogin(t.TempDir(), "j1", "a1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(jobHome.Path) }()
	if jobHome.SyncsLogin {
		t.Fatal("a login without a refresh token was marked for sync")
	}
}

func TestPrepareHomeWithLoginDoesNotSyncTheKeychain(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude.json"), `{"user":"u"}`, 0o600)
	stubKeychain(t, "darwin", refreshableHostLogin)
	jobHome, err := PrepareHomeWithLogin(t.TempDir(), "j1", "a1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(jobHome.Path) }()
	if jobHome.SyncsLogin || jobHome.ClaudeConfigDir != "" {
		t.Fatalf("keychain login marked for sync: %+v", jobHome)
	}
}

func TestPrepareHomeWithLoginDoesNotSyncOtherProviders(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".codex", "auth.json"), `{"key":"codex"}`, 0o600)
	jobHome, err := PrepareHomeWithLogin(t.TempDir(), "j1", "a1", "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(jobHome.Path) }()
	if jobHome.SyncsLogin {
		t.Fatal("codex login marked for sync")
	}
}
