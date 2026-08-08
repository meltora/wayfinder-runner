package runlock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if _, err := Acquire(dir); err == nil {
		t.Fatal("second runner acquired the same state directory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	_ = second.Close()
}

func TestAcquireRejectsStateDirectorySymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(link); err == nil {
		t.Fatal("state-directory symlink was accepted")
	}
}
