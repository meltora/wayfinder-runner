package isolation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveOwnedTreeRemovesLockedDirectoriesFilesAndMissingPaths(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	locked := filepath.Join(tree, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{tree, file, filepath.Join(root, "missing")} {
		if err := removeOwnedTree(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", path, err)
		}
	}
}

func TestRemoveOwnedTreeReportsAnUninspectablePath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedTree(filepath.Join(file, "child")); err == nil {
		t.Fatal("a path below a regular file was accepted")
	}
}
