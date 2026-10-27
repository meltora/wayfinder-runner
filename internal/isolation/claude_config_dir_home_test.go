package isolation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	hostClaudeCredentials       = `{"account":"host-default"}`
	configuredClaudeCredentials = `{"account":"configured"}`
	configuredClaudeState       = `{"user":"configured"}`
)

func writePrivateTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func installHostClaudeLogin(t *testing.T) string {
	t.Helper()
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude", ".credentials.json"), hostClaudeCredentials, 0o600)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude.json"), `{"user":"host-default"}`, 0o600)
	return hostHome
}

func installConfiguredClaudeLogin(t *testing.T, dir string) {
	t.Helper()
	writePrivateTestFile(t, filepath.Join(dir, ".credentials.json"), configuredClaudeCredentials, 0o600)
	writePrivateTestFile(t, filepath.Join(dir, ".claude.json"), configuredClaudeState, 0o600)
}

func stubKeychain(t *testing.T, goos string, data string) *int {
	t.Helper()
	calls := 0
	previousGOOS, previousKeychain := hostGOOS, readKeychainClaudeCredentials
	hostGOOS = goos
	readKeychainClaudeCredentials = func() ([]byte, error) {
		calls++
		return []byte(data), nil
	}
	t.Cleanup(func() { hostGOOS, readKeychainClaudeCredentials = previousGOOS, previousKeychain })
	return &calls
}

func readJobHomeFile(t *testing.T, home, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

func expectNoJobHomes(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, "job-homes"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("job homes left after failure: %v", entries)
	}
}

func TestPrepareHomeClaudeCopiesTheConfiguredConfigDirectoryLogin(t *testing.T) {
	installHostClaudeLogin(t)
	configDir := filepath.Join(t.TempDir(), "claude-personal")
	installConfiguredClaudeLogin(t, configDir)
	stateDir := t.TempDir()

	home, err := PrepareHome(stateDir, "j1", "a1", "claude", []string{"ANTHROPIC_LOG=debug", "CLAUDE_CONFIG_DIR=" + configDir})
	if err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	defer func() { _ = CleanupHome(home) }()
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != configuredClaudeCredentials {
		t.Fatalf("job home credentials = %q, want the configured account", got)
	}
	if got := readJobHomeFile(t, home, ".claude.json"); got != configuredClaudeState {
		t.Fatalf("job home .claude.json = %q, want the configured account", got)
	}
	for relative, want := range map[string]os.FileMode{".": 0o700, ".claude": 0o700, ".claude/.credentials.json": 0o600, ".claude.json": 0o600} {
		info, err := os.Stat(filepath.Join(home, relative))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %o, want %o", relative, info.Mode().Perm(), want)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != ".claude,.claude.json" {
		t.Fatalf("job home entries = %v, want the unchanged in-container layout", names)
	}
	if got := readJobHomeFile(t, configDir, ".credentials.json"); got != configuredClaudeCredentials {
		t.Fatal("configured source was modified")
	}
}

func TestPrepareHomeClaudeUsesTheLastConfiguredDirectoryLikeTheCapabilityDetector(t *testing.T) {
	installHostClaudeLogin(t)
	stale := filepath.Join(t.TempDir(), "stale")
	writePrivateTestFile(t, filepath.Join(stale, ".credentials.json"), `{"account":"stale"}`, 0o600)
	configDir := filepath.Join(t.TempDir(), "current")
	installConfiguredClaudeLogin(t, configDir)

	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=" + stale, "CLAUDE_CONFIG_DIR=" + configDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != configuredClaudeCredentials {
		t.Fatalf("credentials = %q", got)
	}
}

func TestPrepareHomeClaudeResolvesHomeRelativeAndRelativeConfigDirectories(t *testing.T) {
	hostHome := installHostClaudeLogin(t)
	installConfiguredClaudeLogin(t, filepath.Join(hostHome, ".claude-personal"))
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=~/.claude-personal"})
	if err != nil {
		t.Fatalf("home-relative: %v", err)
	}
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != configuredClaudeCredentials {
		t.Fatalf("home-relative credentials = %q", got)
	}
	_ = CleanupHome(home)

	workingDir := t.TempDir()
	installConfiguredClaudeLogin(t, filepath.Join(workingDir, "relative-login"))
	t.Chdir(workingDir)
	home, err = PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=relative-login"})
	if err != nil {
		t.Fatalf("relative: %v", err)
	}
	defer func() { _ = CleanupHome(home) }()
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != configuredClaudeCredentials {
		t.Fatalf("relative credentials = %q", got)
	}
}

func TestPrepareHomeClaudeResolvesABareTildeToTheHostHome(t *testing.T) {
	hostHome := installHostClaudeLogin(t)
	writePrivateTestFile(t, filepath.Join(hostHome, ".credentials.json"), configuredClaudeCredentials, 0o600)
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=~"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != configuredClaudeCredentials {
		t.Fatalf("credentials = %q", got)
	}
}

func TestPrepareHomeClaudeFailsWhenARelativeConfigDirectoryCannotBeResolved(t *testing.T) {
	installHostClaudeLogin(t)
	stateDir := t.TempDir()
	removed := filepath.Join(t.TempDir(), "removed")
	if err := os.MkdirAll(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(removed)
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("working directory still resolves after removal on this platform")
	}
	home, err := PrepareHome(stateDir, "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=relative-login"})
	if err == nil {
		_ = CleanupHome(home)
		t.Fatal("an unresolvable configured directory was accepted")
	}
	if !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR relative-login") {
		t.Fatalf("error %q does not name the configured directory", err)
	}
	expectNoJobHomes(t, stateDir)
}

func TestPrepareHomeFailsWhenTheStateDirectoryIsNotADirectory(t *testing.T) {
	installHostClaudeLogin(t)
	stateFile := filepath.Join(t.TempDir(), "state")
	writePrivateTestFile(t, stateFile, "", 0o600)
	if _, err := PrepareHome(stateFile, "j1", "a1", "claude", nil); err == nil {
		t.Fatal("a regular-file state directory was accepted")
	}
	if err := CleanupHome(""); err != nil {
		t.Fatalf("CleanupHome of an empty path: %v", err)
	}
}

func TestPrepareHomeClaudeNeverFallsBackWhenTheConfiguredLoginIsUnusable(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"missing directory": func(*testing.T, string) {},
		"missing credentials": func(t *testing.T, dir string) {
			writePrivateTestFile(t, filepath.Join(dir, ".claude.json"), configuredClaudeState, 0o600)
		},
		"group readable credentials": func(t *testing.T, dir string) {
			writePrivateTestFile(t, filepath.Join(dir, ".credentials.json"), configuredClaudeCredentials, 0o640)
		},
		"credentials directory": func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, ".credentials.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked credentials": func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "elsewhere.json")
			writePrivateTestFile(t, target, configuredClaudeCredentials, 0o600)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, ".credentials.json")); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked state": func(t *testing.T, dir string) {
			writePrivateTestFile(t, filepath.Join(dir, ".credentials.json"), configuredClaudeCredentials, 0o600)
			target := filepath.Join(t.TempDir(), "state.json")
			writePrivateTestFile(t, target, configuredClaudeState, 0o600)
			if err := os.Symlink(target, filepath.Join(dir, ".claude.json")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			installHostClaudeLogin(t)
			keychainCalls := stubKeychain(t, "darwin", `{"account":"keychain"}`)
			configDir := filepath.Join(t.TempDir(), "claude-personal")
			arrange(t, configDir)
			stateDir := t.TempDir()

			home, err := PrepareHome(stateDir, "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=" + configDir})
			if err == nil {
				_ = CleanupHome(home)
				t.Fatal("an unusable configured login was accepted")
			}
			message := err.Error()
			if !strings.Contains(message, configDir) || !strings.Contains(message, "CLAUDE_CONFIG_DIR") {
				t.Fatalf("error %q does not name the configured directory", message)
			}
			for _, secret := range []string{hostClaudeCredentials, configuredClaudeCredentials, configuredClaudeState, "keychain"} {
				if strings.Contains(message, secret) {
					t.Fatalf("error %q discloses credential contents", message)
				}
			}
			if *keychainCalls != 0 {
				t.Fatalf("keychain consulted %d times with CLAUDE_CONFIG_DIR set", *keychainCalls)
			}
			expectNoJobHomes(t, stateDir)
		})
	}
}

func TestPrepareHomeClaudeCopiesOnlyCredentialsWhenTheConfiguredStateFileIsAbsent(t *testing.T) {
	installHostClaudeLogin(t)
	configDir := filepath.Join(t.TempDir(), "claude-personal")
	writePrivateTestFile(t, filepath.Join(configDir, ".credentials.json"), configuredClaudeCredentials, 0o600)

	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"CLAUDE_CONFIG_DIR=" + configDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != configuredClaudeCredentials {
		t.Fatalf("credentials = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host .claude.json leaked into the job home: %v", err)
	}
}

func TestPrepareHomeClaudeWithoutConfigDirKeepsTheDefaultSourcesAndKeychainFallback(t *testing.T) {
	installHostClaudeLogin(t)
	keychainCalls := stubKeychain(t, "darwin", `{"account":"keychain"}`)
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "claude", []string{"ANTHROPIC_LOG=debug"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != hostClaudeCredentials {
		t.Fatalf("credentials = %q, want the default host login", got)
	}
	if got := readJobHomeFile(t, home, ".claude.json"); got != `{"user":"host-default"}` {
		t.Fatalf(".claude.json = %q", got)
	}
	_ = CleanupHome(home)
	if *keychainCalls != 0 {
		t.Fatal("keychain consulted although a credentials file was copied")
	}

	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	writePrivateTestFile(t, filepath.Join(hostHome, ".claude.json"), `{"user":"host-default"}`, 0o600)
	home, err = PrepareHome(t.TempDir(), "j1", "a1", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	if *keychainCalls != 1 {
		t.Fatalf("keychain calls = %d, want the macOS fallback once", *keychainCalls)
	}
	if got := readJobHomeFile(t, home, ".claude/.credentials.json"); got != `{"account":"keychain"}` {
		t.Fatalf("keychain credentials = %q", got)
	}
}

func TestPrepareHomeIgnoresClaudeConfigDirForOtherProviders(t *testing.T) {
	hostHome := installHostClaudeLogin(t)
	writePrivateTestFile(t, filepath.Join(hostHome, ".codex", "auth.json"), `{"key":"codex"}`, 0o600)
	configDir := filepath.Join(t.TempDir(), "claude-personal")
	installConfiguredClaudeLogin(t, configDir)

	home, err := PrepareHome(t.TempDir(), "j1", "a1", "codex", []string{"CLAUDE_CONFIG_DIR=" + configDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	if got := readJobHomeFile(t, home, ".codex/auth.json"); got != `{"key":"codex"}` {
		t.Fatalf("codex auth = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Claude login copied into a Codex home: %v", err)
	}
}
