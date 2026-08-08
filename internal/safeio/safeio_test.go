package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegular(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRegular(root, "nested/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestOpenRegularRejectsEscapeAndSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", outside, "link", `dir\\file`} {
		if f, err := OpenRegular(root, path); err == nil {
			_ = f.Close()
			t.Fatalf("OpenRegular(%q) unexpectedly succeeded", path)
		}
	}
}

func TestAtomicWriteFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := AtomicWriteFile(root, "nested/state.json", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(root, "nested/state.json", []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "nested", "state.json"))
	if err != nil || string(got) != "two" {
		t.Fatalf("content=%q err=%v", got, err)
	}
	if err := AtomicWriteFile(root, "../escape", []byte("bad"), 0o600); err == nil {
		t.Fatal("path escape accepted")
	}
}

func TestReadRegularFileEnforcesTypeModeAndLimit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	privatePath := filepath.Join(root, "token")
	if err := os.WriteFile(privatePath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRegularFile(privatePath, 32, 0o077)
	if err != nil || string(got) != "secret" {
		t.Fatalf("ReadRegularFile() = %q, %v", got, err)
	}

	if err := os.Chmod(privatePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(privatePath, 32, 0o077); err == nil {
		t.Fatal("group/world-readable file accepted")
	}
	if err := os.Chmod(privatePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(privatePath, 3, 0o077); err == nil {
		t.Fatal("oversized file accepted")
	}

	link := filepath.Join(root, "token-link")
	if err := os.Symlink(privatePath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(link, 32, 0o077); err == nil {
		t.Fatal("symbolic link accepted")
	}
}

func TestMakeTreeRemovableRestoresDirectoryAccess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "file"), []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "a"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := MakeTreeRemovable(root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll after MakeTreeRemovable: %v", err)
	}
}

func TestSyncDirectory(t *testing.T) {
	t.Parallel()
	if err := SyncDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestRemoveRegularAndTypeChecks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRegular(root, "file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "file")); !os.IsNotExist(err) {
		t.Fatalf("file remains: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRegular(root, "dir"); err == nil {
		t.Fatal("directory removed as regular file")
	}
	if err := RemoveRegular(root, "../escape"); err == nil {
		t.Fatal("escape removal accepted")
	}
}

func TestPortableRelativePathValidation(t *testing.T) {
	for _, path := range []string{"", ".", "..", "../escape", "/absolute", `windows\\path`, "bad\x00path"} {
		if _, err := cleanRelative(path); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
	if got, err := cleanRelative("nested/file.txt"); err != nil || got != filepath.Join("nested", "file.txt") {
		t.Fatalf("clean=%q err=%v", got, err)
	}
}

func TestReadAndCleanupRejectInvalidRoots(t *testing.T) {
	if _, err := ReadRegularFile("", 10, 0); err == nil {
		t.Fatal("empty read path accepted")
	}
	if _, err := ReadRegularFile("anything", -1, 0); err == nil {
		t.Fatal("negative read limit accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MakeTreeRemovable(file); err == nil {
		t.Fatal("regular file accepted as cleanup root")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := MakeTreeRemovable(link); err == nil {
		t.Fatal("symlink accepted as cleanup root")
	}
}

func TestAtomicWriteAndOpenRejectInvalidRootTypes(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(fileRoot, "child", []byte("x"), 0o600); err == nil {
		t.Fatal("file root accepted for atomic write")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "component"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRegular(root, "component/child"); err == nil {
		t.Fatal("non-directory path component accepted")
	}
	if _, err := OpenRegular(root, "component"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRegular(root, "missing"); err == nil {
		t.Fatal("missing regular file accepted")
	}
}
