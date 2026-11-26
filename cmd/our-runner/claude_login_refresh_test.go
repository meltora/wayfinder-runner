package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/claudelogin"
)

func TestClaudeLoginRefreshPrintsTheSavedLogin(t *testing.T) {
	t.Setenv(claudelogin.RefreshTokenEnv, "fake-refresh")
	t.Setenv(claudelogin.RefreshScopeEnv, "user:inference")
	configDir := filepath.Join(t.TempDir(), "claude-login")
	saved := `{"claudeAiOauth":{"accessToken":"fake-new-access","refreshToken":"fake-new-refresh","expiresAt":900}}`
	var stdout, stderr bytes.Buffer
	code := runClaudeLoginRefresh(nil, &stdout, &stderr, configDir, func(_ context.Context, dir string) error {
		if dir != configDir {
			t.Fatalf("login ran in %s", dir)
		}
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("config dir = %v %v", info, err)
		}
		return os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(saved), 0o600)
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.String() != saved {
		t.Fatal("the saved login was not printed as is")
	}
}

func TestClaudeLoginRefreshFailsWithoutValues(t *testing.T) {
	failedLogin := exec.Command("sh", "-c", "exit 3").Run()
	for name, test := range map[string]struct {
		args    []string
		grant   bool
		login   claudeAuthLogin
		message string
	}{
		"extra argument": {args: []string{"fake-refresh"}, grant: true, message: "the command takes no arguments"},
		"no grant":       {message: "the refresh grant is missing"},
		"login exits": {grant: true, message: "claude auth login exited with status 3",
			login: func(context.Context, string) error { return failedLogin }},
		"login does not start": {grant: true, message: "claude auth login did not run",
			login: func(context.Context, string) error { return errors.New("fake-refresh not found") }},
		"nothing saved": {grant: true, message: "claude auth login saved no usable login",
			login: func(context.Context, string) error { return nil }},
		"saved without a refresh token": {grant: true, message: "claude auth login saved no usable login",
			login: func(_ context.Context, dir string) error {
				return os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"fake-a","expiresAt":9}}`), 0o600)
			}},
	} {
		t.Run(name, func(t *testing.T) {
			if test.grant {
				t.Setenv(claudelogin.RefreshTokenEnv, "fake-refresh")
				t.Setenv(claudelogin.RefreshScopeEnv, "user:inference")
			} else {
				t.Setenv(claudelogin.RefreshTokenEnv, "")
			}
			var stdout, stderr bytes.Buffer
			code := runClaudeLoginRefresh(test.args, &stdout, &stderr, filepath.Join(t.TempDir(), "c"), test.login)
			if code == 0 || stdout.Len() != 0 {
				t.Fatalf("exit %d stdout %q", code, stdout.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != "claude login refresh failed: "+test.message {
				t.Fatalf("stderr = %q", got)
			}
		})
	}
}
