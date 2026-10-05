package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fingerprintFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	buildWorkspaceGit(t, dir, "init", "-b", "main")
	buildWorkspaceGit(t, dir, "config", "user.email", "runner@example.test")
	buildWorkspaceGit(t, dir, "config", "user.name", "Runner Test")
	writeFixtureFile(t, dir, "README.md", "fixture\n", 0o644)
	writeFixtureFile(t, dir, ".gitignore", "*.log\n", 0o644)
	writeFixtureFile(t, dir, "CLAUDE.md", "repository instructions\n", 0o644)
	if err := os.Symlink("README.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	buildWorkspaceGit(t, dir, "add", "-A")
	buildWorkspaceGit(t, dir, "commit", "-m", "fixture")
	return dir
}

func writeFixtureFile(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeFingerprintIsStableAndDetectsEveryChangeClass(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, dir string){
		"add file":          func(t *testing.T, dir string) { writeFixtureFile(t, dir, "new.txt", "x", 0o644) },
		"change file":       func(t *testing.T, dir string) { writeFixtureFile(t, dir, "README.md", "changed\n", 0o644) },
		"same size change":  func(t *testing.T, dir string) { writeFixtureFile(t, dir, "README.md", "FIXTURE\n", 0o644) },
		"chmod file":        func(t *testing.T, dir string) { _ = os.Chmod(filepath.Join(dir, "README.md"), 0o755) },
		"add ignored file":  func(t *testing.T, dir string) { writeFixtureFile(t, dir, "debug.log", "x", 0o644) },
		"edit info exclude": func(t *testing.T, dir string) { writeFixtureFile(t, dir, ".git/info/exclude", "secret/\n", 0o644) },
		"edit git config":   func(t *testing.T, dir string) { buildWorkspaceGit(t, dir, "config", "core.hooksPath", "/tmp/hooks") },
		"switch head":       func(t *testing.T, dir string) { buildWorkspaceGit(t, dir, "checkout", "-q", "-b", "other") },
		"add settings":      func(t *testing.T, dir string) { writeFixtureFile(t, dir, ".claude/settings.local.json", "{}", 0o644) },
		"retarget symlink": func(t *testing.T, dir string) {
			_ = os.Remove(filepath.Join(dir, "link.md"))
			_ = os.Symlink("CLAUDE.md", filepath.Join(dir, "link.md"))
		},
		"add empty directory": func(t *testing.T, dir string) { _ = os.Mkdir(filepath.Join(dir, "empty"), 0o755) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := fingerprintFixture(t)
			first, err := worktreeFingerprint(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			again, err := worktreeFingerprint(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			if first != again || len(first) != 64 {
				t.Fatalf("fingerprint is unstable: %s != %s", first, again)
			}
			mutate(t, dir)
			changed, err := worktreeFingerprint(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			if changed == first {
				t.Fatalf("%s did not change the fingerprint", name)
			}
		})
	}
}

func TestWorktreeFingerprintIgnoresGitInternalsOutsideItsInputs(t *testing.T) {
	dir := fingerprintFixture(t)
	first, err := worktreeFingerprint(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, dir, ".git/runner-scratch", "x", 0o644)
	second, err := worktreeFingerprint(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("a file outside the fingerprint inputs inside .git changed it")
	}
}

func TestWorktreeFingerprintFailsOutsideAGitCheckout(t *testing.T) {
	if _, err := worktreeFingerprint(context.Background(), t.TempDir()); err == nil {
		t.Fatal("fingerprint of a non-repository succeeded")
	}
}
