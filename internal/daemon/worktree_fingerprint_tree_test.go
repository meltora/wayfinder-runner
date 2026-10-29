package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingHash struct {
	hash.Hash
	written bytes.Buffer
	onWrite func(written string)
}

func newRecordingHash() *recordingHash {
	return &recordingHash{Hash: sha256.New()}
}

func (r *recordingHash) Write(p []byte) (int, error) {
	r.written.Write(p)
	if r.onWrite != nil {
		r.onWrite(r.written.String())
	}
	return r.Hash.Write(p)
}

func expectedTreeEntry(t *testing.T, root, rel, suffix string) string {
	t.Helper()
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", rel, info.Mode().String(), info.Size(), suffix)
}

func TestFingerprintTreeWritesTheExactByteStream(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "a/b.txt", "nested\n", 0o644)
	writeFixtureFile(t, root, ".git/HEAD", "ref: refs/heads/main\n", 0o644)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a/b.txt", filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	recorder := newRecordingHash()
	if err := fingerprintTree(root, recorder); err != nil {
		t.Fatal(err)
	}
	want := expectedTreeEntry(t, root, ".", "other\x00") +
		expectedTreeEntry(t, root, "a", "other\x00") +
		expectedTreeEntry(t, root, "a/b.txt", fmt.Sprintf("file\x00%x\x00", sha256.Sum256([]byte("nested\n")))) +
		expectedTreeEntry(t, root, "dir-link", "link\x00a\x00") +
		expectedTreeEntry(t, root, "empty", "other\x00") +
		expectedTreeEntry(t, root, "file-link", "link\x00a/b.txt\x00")
	if got := recorder.written.String(); got != want {
		t.Fatalf("tree byte stream\n got %q\nwant %q", got, want)
	}
}

func TestFingerprintTreeNeverDigestsOutsideFilesAfterADirectorySwap(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFixtureFile(t, root, "sub/secret.txt", "inside\n", 0o644)
	writeFixtureFile(t, outside, "secret.txt", "outside secret\n", 0o644)
	outsideDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("outside secret\n")))
	recorder := newRecordingHash()
	swapped := false
	recorder.onWrite = func(written string) {
		if swapped || !strings.HasSuffix(written, "\x00") || !strings.Contains(written, "sub/secret.txt\x00") {
			return
		}
		swapped = true
		if err := os.Rename(filepath.Join(root, "sub"), filepath.Join(t.TempDir(), "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
			t.Fatal(err)
		}
	}
	_ = fingerprintTree(root, recorder)
	if !swapped {
		t.Fatal("the swap was never triggered")
	}
	if strings.Contains(recorder.written.String(), outsideDigest) {
		t.Fatal("fingerprint digested a file outside the worktree")
	}
}

func TestFingerprintTreeRefusesAnEscapingDirectoryLink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFixtureFile(t, outside, "secret.txt", "outside secret\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	recorder := newRecordingHash()
	if err := fingerprintTree(root, recorder); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recorder.written.String(), "secret.txt") {
		t.Fatal("fingerprint followed a directory link")
	}
}

func TestWorktreeFingerprintCoversEntriesAfterAGitFileInALinkedWorktree(t *testing.T) {
	ctx := context.Background()
	main := fingerprintFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	buildWorkspaceGit(t, main, "worktree", "add", "-q", "-b", "linked", linked)
	writeFixtureFile(t, linked, "debug.log", "first\n", 0o644)
	first, err := worktreeFingerprint(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	again, err := worktreeFingerprint(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("linked worktree fingerprint is unstable")
	}
	writeFixtureFile(t, linked, "debug.log", "other\n", 0o644)
	changed, err := worktreeFingerprint(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("an ignored file after the .git file did not change the linked worktree fingerprint")
	}
}
