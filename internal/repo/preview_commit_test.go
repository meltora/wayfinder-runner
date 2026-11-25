package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func unpushedPreviewCommit(t *testing.T, origin string) (string, string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(origin, "feature.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "checkout", "-q", "-b", "feature")
	git(t, origin, "add", ".")
	git(t, origin, "commit", "-q", "-m", "feature")
	sha := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	git(t, origin, "update-ref", PreviewExportRefPrefix+sha, sha)
	bundle := filepath.Join(t.TempDir(), "preview.bundle")
	git(t, origin, "bundle", "create", "--quiet", bundle, PreviewExportRefPrefix+sha)
	git(t, origin, "update-ref", "-d", PreviewExportRefPrefix+sha)
	git(t, origin, "checkout", "-q", "main")
	git(t, origin, "branch", "-q", "-D", "feature")
	return sha, bundle
}

func TestImportPreviewCommitAddsOnlyThePreviewRef(t *testing.T) {
	origin := seedOrigin(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	ctx := context.Background()
	mirror, err := m.Register(ctx, origin)
	if err != nil {
		t.Fatal(err)
	}
	sha, bundle := unpushedPreviewCommit(t, origin)
	if m.HasCommit(ctx, origin, sha) {
		t.Fatal("mirror already holds the unpushed commit")
	}
	heads := gitOutputForTest(t, mirror, "for-each-ref", "refs/heads")
	if err := m.ImportPreviewCommit(ctx, origin, bundle, sha); err != nil {
		t.Fatal(err)
	}
	if !m.HasCommit(ctx, origin, sha) {
		t.Fatal("mirror lacks the imported commit")
	}
	if got := gitOutputForTest(t, mirror, "rev-parse", PreviewMirrorRefPrefix+sha); got != sha {
		t.Fatalf("preview ref = %q want %q", got, sha)
	}
	if after := gitOutputForTest(t, mirror, "for-each-ref", "refs/heads"); after != heads {
		t.Fatalf("refs/heads changed:\n%s\n%s", heads, after)
	}
}

func TestImportPreviewCommitRejectsWrongCommitOrInput(t *testing.T) {
	origin := seedOrigin(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	ctx := context.Background()
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	sha, bundle := unpushedPreviewCommit(t, origin)
	if err := m.ImportPreviewCommit(ctx, origin, bundle, strings.Repeat("a", 40)); err == nil {
		t.Fatal("imported a bundle under another commit id")
	}
	if err := m.ImportPreviewCommit(ctx, origin, bundle, "HEAD"); err == nil {
		t.Fatal("accepted a symbolic commit")
	}
	if err := m.ImportPreviewCommit(ctx, origin, filepath.Join(t.TempDir(), "missing"), sha); err == nil {
		t.Fatal("accepted a missing bundle")
	}
	if err := m.ImportPreviewCommit(ctx, seedOrigin(t), bundle, sha); err == nil {
		t.Fatal("imported into an unregistered mirror")
	}
}
