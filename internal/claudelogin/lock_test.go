package claudelogin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fastLock() LockOptions {
	return LockOptions{Retry: 5 * time.Millisecond, Timeout: 80 * time.Millisecond, Update: 10 * time.Millisecond}
}

func configDirForLock(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "claude-config")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lockPaths(dir string) (string, string) {
	return filepath.Join(dir, ".oauth_refresh.lock"), dir + ".lock"
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func age(t *testing.T, path string, by time.Duration) {
	t.Helper()
	old := time.Now().Add(-by)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestLockConfigDirTakesBothClaudeCodeLocks(t *testing.T) {
	dir := configDirForLock(t)
	unlock, err := LockConfigDir(context.Background(), dir, fastLock())
	if err != nil {
		t.Fatal(err)
	}
	primary, legacy := lockPaths(dir)
	for _, path := range []string{primary, legacy} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("lock %s missing: %v", path, err)
		}
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if exists(t, primary) || exists(t, legacy) {
		t.Fatal("locks left behind after unlock")
	}
	if err := unlock(); err != nil {
		t.Fatalf("second unlock: %v", err)
	}
}

func TestLockConfigDirWaitsForAFreshForeignLockAndNeverRemovesIt(t *testing.T) {
	for _, which := range []string{"primary", "legacy"} {
		t.Run(which, func(t *testing.T) {
			dir := configDirForLock(t)
			primary, legacy := lockPaths(dir)
			held := primary
			if which == "legacy" {
				held = legacy
			}
			if err := os.Mkdir(held, 0o700); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			_, err := LockConfigDir(context.Background(), dir, fastLock())
			if !errors.Is(err, ErrLocked) {
				t.Fatalf("err = %v, want ErrLocked", err)
			}
			if time.Since(started) < 80*time.Millisecond {
				t.Fatal("gave up before the timeout")
			}
			if !exists(t, held) {
				t.Fatal("a fresh foreign lock was removed")
			}
			if which == "legacy" && exists(t, primary) {
				t.Fatal("the primary lock was not released after the legacy lock failed")
			}
		})
	}
}

func TestLockConfigDirTakesOverAStaleLock(t *testing.T) {
	dir := configDirForLock(t)
	primary, legacy := lockPaths(dir)
	for _, path := range []string{primary, legacy} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		age(t, path, 61*time.Second)
	}
	unlock, err := LockConfigDir(context.Background(), dir, fastLock())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(primary)
	if err != nil || time.Since(info.ModTime()) > 10*time.Second {
		t.Fatalf("stale lock not replaced: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestLockConfigDirLeavesAStaleLockThatAnotherProcessJustTookOver(t *testing.T) {
	dir := configDirForLock(t)
	primary, _ := lockPaths(dir)
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	age(t, primary, 61*time.Second)
	var fresh os.FileInfo
	beforeStaleRename = func(path string) {
		if path != primary || fresh != nil {
			return
		}
		if err := os.Remove(primary); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(primary, 0o700); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(primary)
		if err != nil {
			t.Fatal(err)
		}
		fresh = info
	}
	t.Cleanup(func() { beforeStaleRename = func(string) {} })
	if _, err := LockConfigDir(context.Background(), dir, fastLock()); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked while the other process holds the lock", err)
	}
	current, err := os.Lstat(primary)
	if err != nil || !os.SameFile(current, fresh) {
		t.Fatalf("the other process's fresh lock was removed or replaced: %v", err)
	}
	matches, _ := filepath.Glob(primary + ".stale-*")
	if len(matches) != 0 {
		t.Fatalf("left behind %v", matches)
	}
}

func TestLockConfigDirKeepsALockYoungerThanSixtySeconds(t *testing.T) {
	dir := configDirForLock(t)
	primary, _ := lockPaths(dir)
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	age(t, primary, 50*time.Second)
	if _, err := LockConfigDir(context.Background(), dir, fastLock()); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

func TestLockConfigDirPutsTheLegacyLockNextToTheResolvedDirectory(t *testing.T) {
	real := configDirForLock(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	unlock, err := LockConfigDir(context.Background(), link, fastLock())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	if !exists(t, real+".lock") {
		t.Fatal("legacy lock not at the resolved path")
	}
	if exists(t, link+".lock") {
		t.Fatal("legacy lock at the unresolved path")
	}
}

func TestLockConfigDirRefreshesTheLockWhileHeld(t *testing.T) {
	dir := configDirForLock(t)
	unlock, err := LockConfigDir(context.Background(), dir, fastLock())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	primary, legacy := lockPaths(dir)
	first := map[string]time.Time{}
	for _, path := range []string{primary, legacy} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		first[path] = info.ModTime()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := os.Stat(primary)
		l, _ := os.Stat(legacy)
		if p != nil && l != nil && p.ModTime().After(first[primary]) && l.ModTime().After(first[legacy]) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("lock mtime was not refreshed")
}

func TestUnlockLeavesALockThatSomeoneElseNowHolds(t *testing.T) {
	dir := configDirForLock(t)
	unlock, err := LockConfigDir(context.Background(), dir, fastLock())
	if err != nil {
		t.Fatal(err)
	}
	primary, _ := lockPaths(dir)
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if !exists(t, primary) {
		t.Fatal("unlock removed a lock it did not create")
	}
}

func TestLockConfigDirStopsOnCancel(t *testing.T) {
	dir := configDirForLock(t)
	primary, _ := lockPaths(dir)
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options := fastLock()
	options.Timeout = 10 * time.Second
	if _, err := LockConfigDir(ctx, dir, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLockOptionsDefaults(t *testing.T) {
	options := LockOptions{}.withDefaults()
	if options.Stale != 60*time.Second || options.Update != 5*time.Second || options.Timeout != 10*time.Second || options.Retry != 200*time.Millisecond || options.Now == nil {
		t.Fatalf("defaults = %+v", options)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(dir, ".credentials.json", []byte("new"), os.Getuid()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil || string(data) != "new" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	info, _ := os.Stat(filepath.Join(dir, ".credentials.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := WriteFileAtomic(dir, ".credentials.json", []byte("again"), -1); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestWriteFileAtomicRejectsPathsOutsideTheDirectory(t *testing.T) {
	for _, name := range []string{"../escape", "/abs", "a/b"} {
		if err := WriteFileAtomic(t.TempDir(), name, []byte("x"), -1); err == nil {
			t.Fatalf("%q accepted", name)
		}
	}
}

func TestWriteFileAtomicLeavesNoTemporaryFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(dir, ".credentials.json", []byte("new"), -1); err == nil {
		t.Fatal("rename over a non-empty directory succeeded")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}
