package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareHomeClaude(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	if err := os.MkdirAll(filepath.Join(hostHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostHome, ".claude", ".credentials.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostHome, ".claude.json"), []byte(`{"user":"u"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	home, err := PrepareHome(stateDir, "j1", "a1", "claude")
	if err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	defer func() { _ = CleanupHome(home) }()
	if filepath.Dir(filepath.Dir(home)) != stateDir {
		t.Fatalf("job home %q is not under state dir %q", home, stateDir)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil || string(data) != `{"token":"x"}` {
		t.Fatalf("credentials copy: %q %v", data, err)
	}
	info, _ := os.Stat(filepath.Join(home, ".claude", ".credentials.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	hostData, _ := os.ReadFile(filepath.Join(hostHome, ".claude", ".credentials.json"))
	if string(hostData) != string(data) {
		t.Fatal("host file should be unchanged")
	}
}

func TestPrepareHomeRejectsCredentialSymlink(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	if err := os.MkdirAll(filepath.Join(hostHome, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(secret, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(hostHome, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareHome(t.TempDir(), "j1", "a1", "codex"); err == nil {
		t.Fatal("credential symlink was accepted")
	}
}

func TestPrepareHomeMissingCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := PrepareHome(t.TempDir(), "j1", "a1", "claude"); err == nil {
		t.Fatal("expected error when no credentials exist")
	}
}

func TestPrepareHomeCodex(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	if err := os.MkdirAll(filepath.Join(hostHome, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostHome, ".codex", "auth.json"), []byte(`{"key":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := PrepareHome(t.TempDir(), "j1", "a1", "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal("codex auth not copied")
	}
	for _, profile := range []string{"runner-plan.config.toml", "runner-build.config.toml"} {
		data, err := os.ReadFile(filepath.Join(home, ".codex", profile))
		if err != nil {
			t.Fatalf("read %s: %v", profile, err)
		}
		text := string(data)
		if !strings.Contains(text, `"/home/runner/.codex" = "deny"`) || !strings.Contains(text, "enabled = false") {
			t.Fatalf("profile %s is not credential/network isolated: %s", profile, text)
		}
	}
}
