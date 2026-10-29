package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var (
	zwnj   = string(rune(0x200c))
	zwj    = string(rune(0x200d))
	lrm    = string(rune(0x200e))
	iss    = string(rune(0x206a))
	bom    = string(rune(0xfeff))
	eAcute = string(rune(0xe9))
)

func TestTrustedResultRepositoryProtectsAgainstGitLookalikes(t *testing.T) {
	f := newAgentResultTest(t)
	trusted := f.repository().Trusted
	for _, gitDir := range []string{trusted.gitDir, trusted.sparseGitDir} {
		for _, key := range []string{"core.protectHFS", "core.protectNTFS"} {
			if got := trustedGitForTest(t, gitDir, "config", "--local", "--get", key); got != "true" {
				t.Fatalf("%s in %s = %q, want true", key, filepath.Base(gitDir), got)
			}
		}
	}
}

func TestCopyRefusesGitLookalikeNames(t *testing.T) {
	names := []string{
		".g" + zwnj + "it", ".git" + zwj, bom + ".git", ".gi" + iss + "t", "git~1", "GIT~1", ".git.", ".git ", ".git..",
		".git . ", ".git:stream", ".GIT.", "sub/.Git.", "sub/git~1",
	}
	for _, name := range names {
		t.Run("directory "+name, func(t *testing.T) {
			f := newAgentResultTest(t)
			writeAgentFile(t, f.checkout, name+"/config", "[core]\n\tfsmonitor = /bin/true\n")
			writeAgentFile(t, f.checkout, name+"/HEAD", "ref: refs/heads/main\n")
			writeAgentFile(t, f.checkout, "feature.txt", "real\n")
			assertRefused(t, f, "Git treats as .git")
		})
		t.Run("file "+name, func(t *testing.T) {
			f := newAgentResultTest(t)
			writeAgentFile(t, f.checkout, name, "gitdir: /elsewhere\n")
			assertRefused(t, f, "Git treats as .git")
		})
	}
}

func TestCopyPublishesNamesThatOnlyResembleGit(t *testing.T) {
	f := newAgentResultTest(t)
	names := []string{".gitignore", ".github/workflow.yml", "git~2", ".gitx", "x.git", ".git-blame-ignore-revs", "git", ".gi", "sub/.gitkeep"}
	for _, name := range names {
		writeAgentFile(t, f.checkout, name, "content\n")
	}

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	for _, name := range names {
		if !slices.Contains(paths, name) {
			t.Fatalf("%s missing from %v", name, paths)
		}
	}
}

func TestCopyRefusesGitmodulesSymlinks(t *testing.T) {
	for _, name := range []string{".gitmodules", ".GITMODULES", ".gitmodules.", ".gitmodules ", "gitmod~1", "GI7EBA~3", ".gitmodules" + zwnj, "sub/.gitmodules"} {
		t.Run(name, func(t *testing.T) {
			f := newAgentResultTest(t)
			writeAgentFile(t, f.checkout, "feature.txt", "real\n")
			link := filepath.Join(f.checkout, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/etc/passwd", link); err != nil {
				t.Fatal(err)
			}
			assertRefused(t, f, "symbolic link Git refuses to store")
		})
	}
}

func TestCopyPublishesARegularGitmodulesFileAndOtherMetadataSymlinks(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitmodules", "")
	writeAgentFile(t, f.checkout, "target.txt", "target\n")
	if err := os.Symlink("target.txt", filepath.Join(f.checkout, ".mailmap")); err != nil {
		t.Fatal(err)
	}

	sha := f.publish(t)

	if mode, _ := treeEntry(t, f, sha, ".mailmap"); mode != "120000" {
		t.Fatalf(".mailmap mode = %q", mode)
	}
	if mode, _ := treeEntry(t, f, sha, ".gitmodules"); mode != "100644" {
		t.Fatalf(".gitmodules mode = %q", mode)
	}
}

func TestGitLookalikeNameRules(t *testing.T) {
	for name, want := range map[string]bool{
		".git": true, ".GiT": true, ".g" + lrm + "it": true, ".git" + bom: true, ".git\\x": true, "git~1.": true,
		".gitignore": false, ".git~1": false, "git~10": false, ".gi" + eAcute + "t": false, "": false, ".g": false,
	} {
		if got := gitLookalikeName(name, ".git", "git~1"); got != want {
			t.Errorf("gitLookalikeName(%q) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		".gitmodules": true, "GITMOD~4": true, "gitmod~5": false, "gi7eba~9": true, "gi7eba~0": false, ".gitmodulesx": false,
	} {
		if got := gitmodulesLookalike(name); got != want {
			t.Errorf("gitmodulesLookalike(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCopyBoundsDefaultToTheRunnerLimits(t *testing.T) {
	f := newAgentResultTest(t)
	trusted := f.repository().Trusted
	if trusted.depthLimit != maxAgentResultDepth || maxAgentResultDepth != 256 {
		t.Fatalf("depth limit = %d (constant %d)", trusted.depthLimit, maxAgentResultDepth)
	}
	if trusted.pathLimit != maxAgentResultPathBytes || maxAgentResultPathBytes != maxWorktreePointerBytes-1 {
		t.Fatalf("path limit = %d (constant %d)", trusted.pathLimit, maxAgentResultPathBytes)
	}
	if trusted.resolveTimeout != agentResultResolveTimeout || agentResultResolveTimeout <= 0 {
		t.Fatalf("resolve timeout = %s (constant %s)", trusted.resolveTimeout, agentResultResolveTimeout)
	}
}

func TestCopyRefusesTreesThatAreTooDeep(t *testing.T) {
	t.Run("over the limit", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.repository().Trusted.depthLimit = 3
		writeAgentFile(t, f.checkout, "a/b/c/d/leaf.txt", "leaf\n")
		assertRefused(t, f, "more than 3 directories deep under a")
	})
	t.Run("at the limit", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.repository().Trusted.depthLimit = 3
		writeAgentFile(t, f.checkout, "a/b/c/leaf.txt", "leaf\n")
		if sha := f.publish(t); !slices.Contains(f.trustedPaths(t, sha), "a/b/c/leaf.txt") {
			t.Fatal("tree at the depth limit was not published")
		}
	})
}

func TestCopyRefusesPathsThatAreTooLong(t *testing.T) {
	t.Run("over the limit", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.repository().Trusted.pathLimit = 20
		writeAgentFile(t, f.checkout, "abcdefghij/klmnopqrstuvwxyz.txt", "long\n")
		assertRefused(t, f, "longer than 20 bytes under abcdefghij")
	})
	t.Run("at the limit", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.repository().Trusted.pathLimit = 20
		writeAgentFile(t, f.checkout, "abcdefghij/klmno.txt", "fits\n")
		if sha := f.publish(t); !slices.Contains(f.trustedPaths(t, sha), "abcdefghij/klmno.txt") {
			t.Fatal("path at the length limit was not published")
		}
	})
}

func TestCopyRefusesADeepDirectoryChainQuickly(t *testing.T) {
	f := newAgentResultTest(t)
	fd, err := unix.Open(f.checkout, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 1200 {
		if err := unix.Mkdirat(fd, "dd", 0o700); err != nil {
			t.Fatal(err)
		}
		next, err := unix.Openat(fd, "dd", unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_ = unix.Close(fd)
		fd = next
	}
	_ = unix.Close(fd)
	started := time.Now()

	assertRefused(t, f, "more than 256 directories deep under dd")

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("deep chain refusal took %s", elapsed)
	}
}

func TestResolutionIsBoundedByATimeout(t *testing.T) {
	f := newAgentResultTest(t)
	f.repository().Trusted.resolveTimeout = time.Nanosecond
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	assertRefused(t, f, "took longer than")
}

func TestCopyStopsWhenTheContextEnds(t *testing.T) {
	f := newAgentResultTest(t)
	copier, source := newCopyHarness(t, f)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	ctx, cancel := context.WithCancel(context.Background())
	copier.ctx = ctx
	cancel()

	err := copier.directory(source, ".")

	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("err = %v, want the context error", err)
	}
}

func newCopyHarness(t *testing.T, f agentResultTest) (*candidateCopy, *os.Root) {
	t.Helper()
	trusted := f.repository().Trusted
	ctx := context.Background()
	if err := os.RemoveAll(trusted.filesDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(trusted.filesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.git(ctx, "read-tree", f.base); err != nil {
		t.Fatal(err)
	}
	target, err := os.OpenRoot(trusted.filesDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	oracle, err := trusted.startIgnoreOracle(ctx, trusted.gitDir, trusted.filesDir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oracle.close() })
	source, err := os.OpenRoot(f.checkout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return &candidateCopy{
		ctx: ctx, trusted: trusted, target: target, oracle: oracle,
		repositoryID: f.repository().ID, label: "the checkout",
	}, source
}
