package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloneTrustedRepositoryClonesTheMirrorAtTheExactCommit(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	expected := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "results", "repo-1", "repo.git")
	if err := m.CloneTrustedRepository(ctx, origin, expected, dest); err != nil {
		t.Fatal(err)
	}
	if got := gitOutputForTest(t, dest, "rev-parse", "--verify", expected+"^{commit}"); got != expected {
		t.Fatalf("trusted clone resolves %q, want %s", got, expected)
	}
	if bare := gitOutputForTest(t, dest, "rev-parse", "--is-bare-repository"); bare != "true" {
		t.Fatalf("trusted clone is not bare: %s", bare)
	}
	if err := m.CloneTrustedRepository(ctx, origin, expected, dest); err == nil {
		t.Fatal("existing trusted destination was accepted")
	}
}

func TestCloneTrustedRepositoryRefusesUnknownCommitsAndRelativePaths(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	expected := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"", "abc", strings.Repeat("f", 40)} {
		dest := filepath.Join(t.TempDir(), "repo.git")
		if err := m.CloneTrustedRepository(ctx, origin, sha, dest); err == nil {
			t.Fatalf("commit %q accepted", sha)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("failed clone left %s behind: %v", dest, err)
		}
	}
	if err := m.CloneTrustedRepository(ctx, origin, expected, "relative/repo.git"); err == nil {
		t.Fatal("relative trusted destination accepted")
	}
	if err := m.CloneTrustedRepository(ctx, "ext::sh -c id", expected, filepath.Join(t.TempDir(), "repo.git")); err == nil {
		t.Fatal("unsafe origin accepted")
	}
}
