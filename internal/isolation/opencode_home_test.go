package isolation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareHomeOpenCodeCopiesOnlyAuthentication(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	authDir := filepath.Join(hostHome, ".local", "share", "opencode")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(`{"customer":{"type":"api","key":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(hostHome, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte(`{"plugin":["untrusted"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	home, err := PrepareHome(t.TempDir(), "j1", "a1", "opencode", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CleanupHome(home) }()
	info, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "auth.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("OpenCode auth copy: mode=%v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "opencode.json")); !os.IsNotExist(err) {
		t.Fatal("host OpenCode config was copied into the isolated job home")
	}
}
