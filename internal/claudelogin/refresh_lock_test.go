package claudelogin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockRefreshExcludesASecondHolderUntilReleased(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockRefresh(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := LockRefresh(ctx, dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second holder err = %v, want ErrLocked", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := LockRefresh(context.Background(), dir)
	if err != nil {
		t.Fatalf("lock not free after release: %v", err)
	}
	if err := again(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(dir, refreshLockFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %v, err = %v", info, err)
	}
}

func TestLockRefreshRefusesASymlinkedLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(dir, refreshLockFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := LockRefresh(context.Background(), dir); err == nil {
		t.Fatal("a symlinked lock file was followed")
	}
}
