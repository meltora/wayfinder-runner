package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetPausedStateRoundTrip(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "runner")
	if err := SetPausedState(stateDir, true); err != nil {
		t.Fatal(err)
	}
	paused, err := ReadPausedState(stateDir)
	if err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if err := SetPausedState(stateDir, false); err != nil {
		t.Fatal(err)
	}
	paused, err = ReadPausedState(stateDir)
	if err != nil || paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
}

func TestReadPausedStateRejectsSymlink(t *testing.T) {
	stateDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(stateDir, "paused")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPausedState(stateDir); err == nil {
		t.Fatal("pause-state symlink accepted")
	}
}
