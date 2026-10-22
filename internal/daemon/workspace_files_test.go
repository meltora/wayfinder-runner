package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

func TestOpenInspectedWorkspaceFile(t *testing.T) {
	t.Run("opens the inspected file", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "a.txt", "a\n")
		info, err := root.Lstat("a.txt")
		if err != nil {
			t.Fatal(err)
		}
		file, err := openInspectedWorkspaceFile(root, "a.txt", os.O_RDONLY, info)
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	})
	t.Run("refuses a missing file", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "a.txt", "a\n")
		info, _ := root.Lstat("a.txt")
		_ = os.Remove(filepath.Join(dir, "a.txt"))
		if _, err := openInspectedWorkspaceFile(root, "a.txt", os.O_RDONLY, info); err == nil {
			t.Fatal("a missing file opened")
		}
	})
	t.Run("refuses a symlink swapped in", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "a.txt", "a\n")
		writeAgentFile(t, dir, "b.txt", "b\n")
		info, _ := root.Lstat("a.txt")
		_ = os.Remove(filepath.Join(dir, "a.txt"))
		if err := os.Symlink("b.txt", filepath.Join(dir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		if _, err := openInspectedWorkspaceFile(root, "a.txt", os.O_RDONLY, info); err == nil {
			t.Fatal("a symlink was followed")
		}
	})
	t.Run("refuses a different file", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "a.txt", "a\n")
		writeAgentFile(t, dir, "b.txt", "b\n")
		info, _ := root.Lstat("a.txt")
		if err := os.Rename(filepath.Join(dir, "b.txt"), filepath.Join(dir, "a.txt")); err != nil {
			t.Fatal(err)
		}
		_, err := openInspectedWorkspaceFile(root, "a.txt", os.O_RDONLY, info)
		if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("refuses a directory", func(t *testing.T) {
		root, dir := openTestRoot(t)
		if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
			t.Fatal(err)
		}
		info, _ := root.Lstat("d")
		if _, err := openInspectedWorkspaceFile(root, "d", os.O_RDONLY, info); err == nil {
			t.Fatal("a directory opened as a file")
		}
	})
}

func TestAppendWorkspaceFile(t *testing.T) {
	t.Run("creates a new file", func(t *testing.T) {
		root, dir := openTestRoot(t)
		if err := appendWorkspaceFile(root, "new.txt", nil, "line\n"); err != nil {
			t.Fatal(err)
		}
		if content, _ := os.ReadFile(filepath.Join(dir, "new.txt")); string(content) != "line\n" {
			t.Fatalf("content = %q", content)
		}
	})
	t.Run("refuses to create over an existing file", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "new.txt", "raced\n")
		if err := appendWorkspaceFile(root, "new.txt", nil, "line\n"); err == nil {
			t.Fatal("an existing file was overwritten")
		}
	})
	t.Run("appends to the inspected file", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "x.txt", "one\n")
		info, _ := root.Lstat("x.txt")
		if err := appendWorkspaceFile(root, "x.txt", info, "two\n"); err != nil {
			t.Fatal(err)
		}
		if content, _ := os.ReadFile(filepath.Join(dir, "x.txt")); string(content) != "one\ntwo\n" {
			t.Fatalf("content = %q", content)
		}
	})
	t.Run("refuses a file that changed", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "x.txt", "one\n")
		writeAgentFile(t, dir, "y.txt", "other\n")
		info, _ := root.Lstat("x.txt")
		if err := os.Rename(filepath.Join(dir, "y.txt"), filepath.Join(dir, "x.txt")); err != nil {
			t.Fatal(err)
		}
		if err := appendWorkspaceFile(root, "x.txt", info, "two\n"); err == nil {
			t.Fatal("a replaced file was appended to")
		}
	})
	t.Run("refuses a hard-linked file", func(t *testing.T) {
		root, dir := openTestRoot(t)
		writeAgentFile(t, dir, "x.txt", "one\n")
		if err := os.Link(filepath.Join(dir, "x.txt"), filepath.Join(dir, "alias.txt")); err != nil {
			t.Fatal(err)
		}
		info, _ := root.Lstat("x.txt")
		err := appendWorkspaceFile(root, "x.txt", info, "two\n")
		if err == nil || !strings.Contains(err.Error(), "more than one link") {
			t.Fatalf("err = %v", err)
		}
		if content, _ := os.ReadFile(filepath.Join(dir, "alias.txt")); string(content) != "one\n" {
			t.Fatalf("hard link target changed: %q", content)
		}
	})
}
