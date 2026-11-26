package isolation

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeStatusFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostClaudeLoginReportsTheFileLoginWithoutValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	expires := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	writeStatusFile(t, filepath.Join(home, ".claude", ".credentials.json"),
		`{"mcpOAuth":{},"claudeAiOauth":{"accessToken":"fake-a","refreshToken":"fake-r","expiresAt":`+strconv.FormatInt(expires.UnixMilli(), 10)+`,"scopes":["user:inference"]}}`)
	status, err := HostClaudeLogin(nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Source != ClaudeLoginFile || !status.HasRefresh || status.AccountKnown || !status.ExpiresAt.Equal(expires) {
		t.Fatalf("status = %+v", status)
	}
	writeStatusFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"acc","organizationUuid":"org"}}`)
	if status, err = HostClaudeLogin(nil); err != nil || !status.AccountKnown {
		t.Fatalf("status = %+v, err = %v with oauthAccount", status, err)
	}
}

func TestHostClaudeLoginUsesTheConfiguredDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "personal")
	writeStatusFile(t, filepath.Join(dir, ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"fake-a","expiresAt":1,"accountUuid":"acc","organizationUuid":"org"}}`)
	status, err := HostClaudeLogin([]string{"CLAUDE_CONFIG_DIR=~/personal"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Source != ClaudeLoginFile || status.HasRefresh || !status.AccountKnown {
		t.Fatalf("status = %+v", status)
	}
}

func TestHostClaudeLoginWithoutAFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	previous := hostGOOS
	t.Cleanup(func() { hostGOOS = previous })
	hostGOOS = "linux"
	if status, err := HostClaudeLogin(nil); err != nil || status.Source != ClaudeLoginNone {
		t.Fatalf("status = %+v, err = %v on linux", status, err)
	}
	hostGOOS = "darwin"
	if status, err := HostClaudeLogin(nil); err != nil || status.Source != ClaudeLoginKeychain {
		t.Fatalf("status = %+v, err = %v on darwin", status, err)
	}
	writeStatusFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":`)
	if _, err := HostClaudeLogin(nil); err == nil {
		t.Fatal("an unparsable login was accepted")
	}
}
