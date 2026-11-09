package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	unreadableEntryRefusal = "has an entry the runner cannot read at "
	changedEntryRefusal    = "changed while it was read at "
)

func requireNonRootPermissions(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission refusals cannot be observed as root")
	}
}

func assertCopyRefusal(t *testing.T, err error, want string) {
	t.Helper()
	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a refusal containing %q", err, want)
	}
	if strings.Contains(err.Error(), string(filepath.Separator)+"tmp") {
		t.Fatalf("refusal discloses a host path: %v", err)
	}
	if len(err.Error()) > 4096 {
		t.Fatalf("refusal is %d bytes", len(err.Error()))
	}
}

func restoreMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	t.Cleanup(func() { _ = os.Chmod(path, mode) })
}

func TestCopyEntryRefusalNamesAnUnreadableEntry(t *testing.T) {
	requireNonRootPermissions(t)
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "locked/inner.txt", "agent\n")
	copier, source := newCopyHarness(t, f)
	locked, err := source.OpenRoot("locked")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Close() }()
	if err := os.Chmod(filepath.Join(f.checkout, "locked"), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreMode(t, filepath.Join(f.checkout, "locked"), 0o700)

	err = copier.entry(locked, "locked", "inner.txt")

	assertCopyRefusal(t, err, unreadableEntryRefusal+"locked/inner.txt")
}

func TestCopyEntryRefusalKeepsTheChangeMessageForAVanishedEntry(t *testing.T) {
	f := newAgentResultTest(t)
	copier, source := newCopyHarness(t, f)

	err := copier.entry(source, ".", "vanished.txt")

	assertCopyRefusal(t, err, changedEntryRefusal+"vanished.txt")
}

func TestCopySubdirectoryRefusalNamesAPrivateDirectory(t *testing.T) {
	requireNonRootPermissions(t)
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "other/inner.txt", "agent\n")
	if err := os.Chmod(filepath.Join(f.checkout, "other"), 0o000); err != nil {
		t.Fatal(err)
	}
	restoreMode(t, filepath.Join(f.checkout, "other"), 0o700)
	copier, source := newCopyHarness(t, f)
	inspected, err := source.Lstat("other")
	if err != nil {
		t.Fatal(err)
	}

	err = copier.subdirectory(source, "other", "other", inspected)

	assertCopyRefusal(t, err, unreadableEntryRefusal+"other")
	if strings.Contains(err.Error(), "changed while it was read") {
		t.Fatalf("a permission failure is reported as a change: %v", err)
	}
}

func TestCopySubdirectoryRefusalKeepsTheChangeMessageForAVanishedDirectory(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "dir/inner.txt", "agent\n")
	copier, source := newCopyHarness(t, f)
	inspected, err := source.Lstat("dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.checkout, "dir")); err != nil {
		t.Fatal(err)
	}

	err = copier.subdirectory(source, "dir", "dir", inspected)

	assertCopyRefusal(t, err, changedEntryRefusal+"dir")
}

func TestCopySubdirectoryRefusalKeepsTheChangeMessageForAReplacedDirectory(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "dir/inner.txt", "agent\n")
	copier, source := newCopyHarness(t, f)
	inspected, err := source.Lstat("dir")
	if err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, f.checkout, "replacement/inner.txt", "replacement\n")
	if err := os.Rename(filepath.Join(f.checkout, "dir"), filepath.Join(f.checkout, "kept")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.checkout, "replacement"), filepath.Join(f.checkout, "dir")); err != nil {
		t.Fatal(err)
	}

	err = copier.subdirectory(source, "dir", "dir", inspected)

	assertCopyRefusal(t, err, changedEntryRefusal+"dir")
}

func TestCopySymlinkRefusalKeepsTheChangeMessageWhenReadlinkFails(t *testing.T) {
	f := newAgentResultTest(t)
	copier, source := newCopyHarness(t, f)

	err := copier.symlink(source, "gone-link", "gone-link")

	assertCopyRefusal(t, err, changedEntryRefusal+"gone-link")
}

func TestCopyFileRefusalNamesAnUnreadableFile(t *testing.T) {
	requireNonRootPermissions(t)
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "private.txt", "agent\n")
	if err := os.Chmod(filepath.Join(f.checkout, "private.txt"), 0o000); err != nil {
		t.Fatal(err)
	}
	restoreMode(t, filepath.Join(f.checkout, "private.txt"), 0o600)
	copier, source := newCopyHarness(t, f)
	inspected, err := source.Lstat("private.txt")
	if err != nil {
		t.Fatal(err)
	}

	err = copier.file(source, "private.txt", "private.txt", inspected)

	assertCopyRefusal(t, err, unreadableEntryRefusal+"private.txt")
}

func TestCopyFileRefusalKeepsTheChangeMessageForAVanishedFile(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "data.txt", "agent\n")
	copier, source := newCopyHarness(t, f)
	inspected, err := source.Lstat("data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.checkout, "data.txt")); err != nil {
		t.Fatal(err)
	}

	err = copier.file(source, "data.txt", "data.txt", inspected)

	assertCopyRefusal(t, err, changedEntryRefusal+"data.txt")
}

func TestPublishRefusalNamesAPrivateDirectoryTheRunnerCannotRead(t *testing.T) {
	requireNonRootPermissions(t)
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	writeAgentFile(t, f.checkout, "other/inner.txt", "agent\n")
	if err := os.Chmod(filepath.Join(f.checkout, "other"), 0o000); err != nil {
		t.Fatal(err)
	}
	restoreMode(t, filepath.Join(f.checkout, "other"), 0o700)

	assertRefused(t, f, unreadableEntryRefusal+"other")
}
