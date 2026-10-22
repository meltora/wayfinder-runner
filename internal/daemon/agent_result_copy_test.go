package daemon

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func treeEntry(t *testing.T, f agentResultTest, commit, path string) (string, string) {
	t.Helper()
	line := f.trustedGit(t, "ls-tree", commit, "--", path)
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return "", ""
	}
	return fields[0], fields[2]
}

func TestCopyPublishesSymlinkEscapeAsASymlink(t *testing.T) {
	f := newAgentResultTest(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("host secret content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(f.checkout, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../..", filepath.Join(f.checkout, "up")); err != nil {
		t.Fatal(err)
	}

	sha := f.publish(t)

	for name, target := range map[string]string{"escape": secret, "up": "../../.."} {
		mode, object := treeEntry(t, f, sha, name)
		if mode != "120000" {
			t.Fatalf("%s mode = %q, want a symlink", name, mode)
		}
		if content := f.trustedGit(t, "cat-file", "blob", object); content != target {
			t.Fatalf("%s target = %q", name, content)
		}
	}
	if grep, err := trustedGitTry(f.repository().Trusted.gitDir, "grep", "-q", "host secret content", sha); err == nil {
		t.Fatalf("symlink target content was published: %s", grep)
	}
}

func TestCopyKeepsTheExecutableBit(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(f.checkout, "run.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, f.checkout, "data.txt", "data\n")

	sha := f.publish(t)

	if mode, _ := treeEntry(t, f, sha, "run.sh"); mode != "100755" {
		t.Fatalf("run.sh mode = %s", mode)
	}
	if mode, _ := treeEntry(t, f, sha, "data.txt"); mode != "100644" {
		t.Fatalf("data.txt mode = %s", mode)
	}
}

func TestCopyPublishesHardLinksByContent(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "original.txt", "linked content\n")
	if err := os.Link(filepath.Join(f.checkout, "original.txt"), filepath.Join(f.checkout, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}

	sha := f.publish(t)

	if content := f.trustedGit(t, "show", sha+":hardlink.txt"); content != "linked content" {
		t.Fatalf("hard link content = %q", content)
	}
}

func TestCopyRefusesSpecialFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{"fifo", func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"socket", func(t *testing.T, path string) {
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Skipf("unix socket unavailable: %v", err)
			}
			t.Cleanup(func() { _ = listener.Close() })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			tc.plant(t, filepath.Join(f.checkout, "s"))
			assertRefused(t, f, "special file s")
		})
	}
}

func TestCopySkipsIgnoredSpecialFilesAndDirectories(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n*.fifo\n")
	writeAgentFile(t, f.checkout, "node_modules/pkg/index.js", "module\n")
	agentGit(t, filepath.Join(f.checkout, "node_modules", "pkg"), "init", "-q")
	if err := syscall.Mkfifo(filepath.Join(f.checkout, "pipe.fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, f.checkout, "app.js", "app\n")

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	if !slices.Contains(paths, "app.js") || !slices.Contains(paths, ".gitignore") {
		t.Fatalf("result tree = %v", paths)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, "node_modules") || strings.HasSuffix(path, ".fifo") {
			t.Fatalf("ignored path published: %v", paths)
		}
	}
}

func TestCopyFollowsGitignoreNegationAndDirectoryOnlyPatterns(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "*.log\n!keep.log\nbuild/\n")
	writeAgentFile(t, f.checkout, "drop.log", "drop\n")
	writeAgentFile(t, f.checkout, "keep.log", "keep\n")
	writeAgentFile(t, f.checkout, "build/out.txt", "out\n")
	writeAgentFile(t, f.checkout, "other/build", "a file named build\n")
	writeAgentFile(t, f.checkout, "sub/.gitignore", "local.txt\n")
	writeAgentFile(t, f.checkout, "sub/local.txt", "local\n")
	writeAgentFile(t, f.checkout, "sub/kept.txt", "kept\n")

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	for _, want := range []string{"keep.log", "other/build", "sub/kept.txt", "sub/.gitignore"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("%s missing from %v", want, paths)
		}
	}
	for _, unwanted := range []string{"drop.log", "build/out.txt", "sub/local.txt"} {
		if slices.Contains(paths, unwanted) {
			t.Fatalf("%s published: %v", unwanted, paths)
		}
	}
}

func TestCopyHonorsTheCandidateExcludeFileAsData(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".git/info/exclude", "secret.env\n!shortstack-artifacts/\n")
	writeAgentFile(t, f.checkout, "secret.env", "TOKEN=1\n")
	writeAgentFile(t, f.checkout, "shortstack-artifacts/report.txt", "runner artifact\n")
	writeAgentFile(t, f.checkout, "visible.txt", "visible\n")

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	if !slices.Contains(paths, "visible.txt") || slices.Contains(paths, "secret.env") {
		t.Fatalf("result tree = %v", paths)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, "shortstack-artifacts") {
			t.Fatalf("runner artifact published: %v", paths)
		}
	}
}

func TestCopyKeepsTrackedFilesThatMatchIgnoreRules(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, ".gitignore", "*.log\n")
		writeAgentFile(t, origin, "tracked.log", "before\n")
		buildWorkspaceGit(t, origin, "add", "-f", "tracked.log")
	})
	writeAgentFile(t, f.checkout, "tracked.log", "after\n")
	if err := os.Remove(filepath.Join(f.checkout, "README.md")); err != nil {
		t.Fatal(err)
	}

	sha := f.publish(t)

	if content := f.trustedGit(t, "show", sha+":tracked.log"); content != "after" {
		t.Fatalf("tracked ignored file = %q", content)
	}
	if slices.Contains(f.trustedPaths(t, sha), "README.md") {
		t.Fatal("deleted file is still in the result")
	}
}

func TestCopyRefusesNestedRepositories(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, checkout string)
	}{
		{"embedded-repository", func(t *testing.T, checkout string) {
			writeAgentFile(t, checkout, "vendor/lib/file.txt", "vendored\n")
			agentGit(t, filepath.Join(checkout, "vendor", "lib"), "init", "-q")
		}},
		{"gitfile", func(t *testing.T, checkout string) {
			writeAgentFile(t, checkout, "sub/.git", "gitdir: /elsewhere\n")
		}},
		{"case-folded", func(t *testing.T, checkout string) {
			writeAgentFile(t, checkout, "sub/.GIT/config", "[core]\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			tc.plant(t, f.checkout)
			assertRefused(t, f, "nested Git repository")
		})
	}
}

func seedGitlink(t *testing.T, origin string) string {
	t.Helper()
	pointer := strings.Repeat("a", 40)
	if err := os.MkdirAll(filepath.Join(origin, "libs", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	buildWorkspaceGit(t, origin, "update-index", "--add", "--cacheinfo", "160000,"+pointer+",libs/sub")
	return pointer
}

func TestCopyKeepsStartCommitGitlinks(t *testing.T) {
	var pointer string
	f := newAgentResultTestWith(t, func(origin string) { pointer = seedGitlink(t, origin) })
	if err := os.MkdirAll(filepath.Join(f.checkout, "libs", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, f.checkout, "libs/sub/.git", "gitdir: /populated/submodule\n")
	writeAgentFile(t, f.checkout, "libs/sub/inner.txt", "submodule content\n")
	writeAgentFile(t, f.checkout, "change.txt", "change\n")

	sha := f.publish(t)

	if mode, object := treeEntry(t, f, sha, "libs/sub"); mode != "160000" || object != pointer {
		t.Fatalf("gitlink = %s %s", mode, object)
	}
	if slices.Contains(f.trustedPaths(t, sha), "libs/sub/inner.txt") {
		t.Fatal("submodule content was published")
	}
}

func TestCopySkipsWorktreeNestedInAWorktreeCandidate(t *testing.T) {
	f := newAgentResultTest(t)
	outer := filepath.Join(f.jr.workDir, "agent-outer")
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-outer", outer)
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-inner", filepath.Join(outer, "inner"))
	writeAgentFile(t, outer, "outer.txt", "outer\n")

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	if !slices.Contains(paths, "outer.txt") {
		t.Fatalf("outer worktree result missing: %v", paths)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, "inner") {
			t.Fatalf("nested worktree published: %v", paths)
		}
	}
}

func TestCopyRefusesWhenLimitsAreExceeded(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.repository().Trusted.entryLimit = 5
		for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
			writeAgentFile(t, f.checkout, name, name)
		}
		assertRefused(t, f, "more than 5 entries")
	})
	t.Run("bytes", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.repository().Trusted.byteLimit = 10
		writeAgentFile(t, f.checkout, "big.txt", strings.Repeat("x", 64))
		assertRefused(t, f, "more than 10 bytes")
	})
}
