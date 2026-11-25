package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func unpushedPreviewCommit(t *testing.T, origin string, excludes ...string) (string, string) {
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
	git(t, origin, append([]string{"bundle", "create", "--quiet", bundle, PreviewExportRefPrefix + sha}, excludes...)...)
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

func TestImportPreviewCommitSurvivesRegisterPrune(t *testing.T) {
	origin := seedOrigin(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	ctx := context.Background()
	mirror, err := m.Register(ctx, origin)
	if err != nil {
		t.Fatal(err)
	}
	sha, bundle := unpushedPreviewCommit(t, origin)
	for range 2 {
		if err := m.ImportPreviewCommit(ctx, origin, bundle, sha); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Register(ctx, origin); err != nil {
			t.Fatal(err)
		}
	}
	if got := gitOutputForTest(t, mirror, "rev-parse", "--verify", PreviewMirrorRefPrefix+sha); got != sha {
		t.Fatalf("preview ref after register = %q want %q", got, sha)
	}
	if !m.HasCommit(ctx, origin, sha) {
		t.Fatal("register pruned the imported commit")
	}
	fetch := strings.Split(gitOutputForTest(t, mirror, "config", "--get-all", "remote.origin.fetch"), "\n")
	if len(fetch) != 2 || fetch[0] != "+refs/*:refs/*" || fetch[1] != "^refs/our-preview/*" {
		t.Fatalf("mirror fetch refspecs = %q", fetch)
	}
	git(t, origin, "commit", "-q", "--allow-empty", "-m", "upstream")
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	if got := gitOutputForTest(t, mirror, "rev-parse", "refs/heads/main"); got != gitOutputForTest(t, origin, "rev-parse", "main") {
		t.Fatalf("register stopped updating heads: %s", got)
	}
}

func TestImportPreviewCommitAcceptsBundleWithBasis(t *testing.T) {
	origin := seedOrigin(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	ctx := context.Background()
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	basis := gitOutputForTest(t, origin, "rev-parse", "main")
	sha, bundle := unpushedPreviewCommit(t, origin, "^"+basis)
	verify := exec.Command("git", "bundle", "verify", bundle)
	verify.Dir = origin
	out, err := verify.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "requires this ref") || !strings.Contains(string(out), basis) {
		t.Fatalf("bundle does not require the basis: %v\n%s", err, out)
	}
	objects := exec.Command("git", "bundle", "list-heads", bundle)
	objects.Dir = origin
	if heads, err := objects.CombinedOutput(); err != nil || strings.Count(strings.TrimSpace(string(heads)), "\n") != 0 {
		t.Fatalf("bundle heads = %s err = %v", heads, err)
	}
	if err := m.ImportPreviewCommit(ctx, origin, bundle, sha); err != nil {
		t.Fatal(err)
	}
	if !m.HasCommit(ctx, origin, sha) {
		t.Fatal("mirror lacks the imported commit")
	}
}

func TestImportPreviewCommitInParallelProtectsRefsOnce(t *testing.T) {
	origin := seedOrigin(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	ctx := context.Background()
	mirror, err := m.Register(ctx, origin)
	if err != nil {
		t.Fatal(err)
	}
	sha, bundle := unpushedPreviewCommit(t, origin)
	errs := make([]error, 6)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { errs[i] = m.ImportPreviewCommit(ctx, origin, bundle, sha) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
	}
	fetch := strings.Split(gitOutputForTest(t, mirror, "config", "--get-all", "remote.origin.fetch"), "\n")
	if len(fetch) != 2 || fetch[1] != "^refs/our-preview/*" {
		t.Fatalf("mirror fetch refspecs = %q", fetch)
	}
}

func TestGitSupportsNegativeRefspecs(t *testing.T) {
	for version, want := range map[string]bool{
		"git version 2.43.0":                 true,
		"git version 2.29.0":                 true,
		"git version 3.0.1":                  true,
		"git version 2.39.3 (Apple Git-145)": true,
		"git version 2.28.4":                 false,
		"git version 1.9.5":                  false,
		"git version 2.windows":              false,
		"not git":                            false,
	} {
		if got := gitSupportsNegativeRefspecs(version); got != want {
			t.Fatalf("%q supported = %v want %v", version, got, want)
		}
	}
}
