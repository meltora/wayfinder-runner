package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func trustedMktree(t *testing.T, gitDir, listing string) string {
	t.Helper()
	command := exec.Command("git", "--git-dir="+gitDir, "mktree")
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	command.Stdin = strings.NewReader(listing)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("mktree: %v %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func trustedCommitWithEntry(t *testing.T, f agentResultTest, parent, entry string) string {
	t.Helper()
	trusted := f.repository().Trusted
	var kept []string
	for _, line := range strings.Split(f.trustedGit(t, "ls-tree", parent+"^{tree}"), "\n") {
		if line != "" && !strings.HasSuffix(line, "\tlibs") && !strings.HasSuffix(line, "\tlib") {
			kept = append(kept, line)
		}
	}
	tree := trustedMktree(t, trusted.gitDir, strings.Join(append(kept, entry), "\n")+"\n")
	commit, err := trusted.commitTree(context.Background(), tree, parent, "crafted")
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func TestGitlinkRefusalRejectsAGitlinkAddedToTheTrustedTree(t *testing.T) {
	f := newAgentResultTest(t)
	crafted := trustedCommitWithEntry(t, f, f.base, "160000 commit "+f.base+"\tlib")

	err := refuseCommittedGitlinks(context.Background(), f.repository(), crafted)

	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "adds or changes a gitlink") {
		t.Fatalf("err = %v, want the gitlink refusal", err)
	}
	if err := refuseCommittedGitlinks(context.Background(), f.repository(), f.base); err != nil {
		t.Fatalf("the base commit was refused: %v", err)
	}
}

func TestGitlinkRefusalRejectsAChangedGitlinkPointer(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) { seedGitlink(t, origin) })
	subtree := trustedMktree(t, f.repository().Trusted.gitDir, "160000 commit "+strings.Repeat("c", 40)+"\tsub\n")
	crafted := trustedCommitWithEntry(t, f, f.base, "040000 tree "+subtree+"\tlibs")

	if err := refuseCommittedGitlinks(context.Background(), f.repository(), crafted); err == nil {
		t.Fatal("a changed gitlink pointer was accepted")
	}
	removed := trustedCommitWithEntry(t, f, f.base, "100644 blob "+f.trustedGit(t, "rev-parse", f.base+":README.md")+"\tlibs")
	if err := refuseCommittedGitlinks(context.Background(), f.repository(), removed); err != nil {
		t.Fatalf("replacing a gitlink with a file was refused: %v", err)
	}
}

func TestGitlinkRefusalFailsClosed(t *testing.T) {
	f := newAgentResultTest(t)
	repository := f.repository()
	if err := refuseCommittedGitlinks(context.Background(), repository, strings.Repeat("d", 40)); err == nil {
		t.Fatal("an unknown result commit was accepted")
	}
	repository.BaseSHA = "not-a-sha"
	if err := refuseCommittedGitlinks(context.Background(), repository, f.base); err == nil {
		t.Fatal("an invalid base was accepted")
	}
	if !rawDiffAddsGitlink("garbage\x00path\x00") {
		t.Fatal("malformed diff output was accepted")
	}
}

func TestPublishRefusesAGitlinkTheStartCommitAddedOverTheBase(t *testing.T) {
	f := newAgentResultTest(t)
	crafted := trustedCommitWithEntry(t, f, f.base, "160000 commit "+f.base+"\tlib")
	repository := &f.jr.repositories[0]
	repository.StartSHA = crafted
	gitlinks, err := repository.Trusted.treeGitlinks(context.Background(), crafted)
	if err != nil || !gitlinks["lib"] {
		t.Fatalf("crafted gitlinks = %v err=%v", gitlinks, err)
	}
	repository.Trusted.gitlinks = gitlinks
	if err := os.Mkdir(filepath.Join(f.checkout, "lib"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")

	_, err = f.jr.publishAgentResult(context.Background(), *repository)

	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "adds or changes a gitlink") {
		t.Fatalf("err = %v, want the gitlink refusal", err)
	}
	if _, refErr := trustedGitTry(repository.Trusted.gitDir, "rev-parse", "--verify", "refs/heads/"+f.branch); refErr == nil {
		t.Fatal("the trusted job branch was moved")
	}
}

func TestChangeSetDistinguishesModeFromContent(t *testing.T) {
	t.Run("same content with a different mode is ambiguous", func(t *testing.T) {
		f := newAgentResultTest(t)
		wt := f.addWorktree(t, ".claude/worktrees/x", "agent-x")
		writeAgentFile(t, f.checkout, "README.md", "same\n")
		writeAgentFile(t, wt, "README.md", "same\n")
		if err := os.Chmod(filepath.Join(wt, "README.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeAgentFile(t, wt, "feature.txt", "real\n")
		assertRefused(t, f, "ambiguous")
	})
	t.Run("a mode-only change is a change", func(t *testing.T) {
		f := newAgentResultTest(t)
		if err := os.Chmod(filepath.Join(f.checkout, "README.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		sha := f.publish(t)
		if mode, _ := treeEntry(t, f, sha, "README.md"); mode != "100755" {
			t.Fatalf("README.md mode = %s", mode)
		}
		trusted := f.repository().Trusted
		changes, err := trusted.changeSet(context.Background(), f.base, f.trustedGit(t, "rev-parse", sha+"^{tree}"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(changes["README.md"], "100755 ") {
			t.Fatalf("change set = %v", changes)
		}
	})
}

func TestTrustedGitIgnoresTheHostGitConfiguration(t *testing.T) {
	home := t.TempDir()
	filter, filterMarker := plantMarkerProgram(t, home, "host-filter")
	monitor, monitorMarker := plantMarkerProgram(t, home, "host-fsmonitor")
	hostConfig := "[filter \"evil\"]\n\tclean = " + filter + "\n\trequired = true\n[core]\n\tfsmonitor = " + monitor + "\n\texcludesFile = " + filepath.Join(home, "ignore-all") + "\n"
	writeAgentFile(t, home, ".gitconfig", hostConfig)
	writeAgentFile(t, home, ".config/git/config", hostConfig)
	writeAgentFile(t, home, "ignore-all", "*.txt\n")
	f := newAgentResultTest(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	writeAgentFile(t, f.checkout, ".gitattributes", "* filter=evil\n")
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")

	sha := f.publish(t)

	assertMarkerAbsent(t, filterMarker, monitorMarker)
	if !slices.Contains(f.trustedPaths(t, sha), "feature.txt") {
		t.Fatal("the host excludesFile hid agent content")
	}
	if got := f.trustedGit(t, "show", sha+":feature.txt"); got != "real" {
		t.Fatalf("feature.txt = %q", got)
	}
}

func TestCopyRefusesASymlinkSwappedInForAFile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(t *testing.T, f agentResultTest) string
	}{
		{"target outside the workspace", func(t *testing.T, f agentResultTest) string {
			dir := t.TempDir()
			writeAgentFile(t, dir, "secret.txt", "HOST-SECRET\n")
			return filepath.Join(dir, "secret.txt")
		}},
		{"target inside the candidate", func(t *testing.T, f agentResultTest) string {
			writeAgentFile(t, f.checkout, "private.txt", "IN-CANDIDATE-SECRET\n")
			return "private.txt"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			target := tc.target(t, f)
			writeAgentFile(t, f.checkout, "data.txt", "regular\n")
			copier, source := newCopyHarness(t, f)
			inspected, err := source.Lstat("data.txt")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(f.checkout, "data.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(f.checkout, "data.txt")); err != nil {
				t.Fatal(err)
			}

			err = copier.file(source, "data.txt", "data.txt", inspected)

			var resultErr *agentResultError
			if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "changed while it was read at data.txt") {
				t.Fatalf("err = %v, want the swap refusal", err)
			}
			if _, statErr := os.Lstat(filepath.Join(copier.trusted.filesDir, "data.txt")); statErr == nil {
				t.Fatal("the swapped entry reached the trusted work tree")
			}
		})
	}
}

func TestCopyRefusesAFileReplacedAfterInspection(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "data.txt", "first\n")
	copier, source := newCopyHarness(t, f)
	inspected, err := source.Lstat("data.txt")
	if err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, f.checkout, "replacement.txt", "second\n")
	if err := os.Rename(filepath.Join(f.checkout, "replacement.txt"), filepath.Join(f.checkout, "data.txt")); err != nil {
		t.Fatal(err)
	}

	if err := copier.file(source, "data.txt", "data.txt", inspected); err == nil || !strings.Contains(err.Error(), "changed while it was read") {
		t.Fatalf("err = %v, want the swap refusal", err)
	}
}

func TestCopyRefusesADirectorySwappedForASymlink(t *testing.T) {
	f := newAgentResultTest(t)
	outside := t.TempDir()
	writeAgentFile(t, outside, "inner.txt", "HOST-SECRET\n")
	writeAgentFile(t, f.checkout, "dir/inner.txt", "agent\n")
	writeAgentFile(t, f.checkout, "other/inner.txt", "agent\n")
	copier, source := newCopyHarness(t, f)
	inspected, err := source.Lstat("dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.checkout, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(f.checkout, "dir")); err != nil {
		t.Fatal(err)
	}

	err = copier.subdirectory(source, "dir", "dir", inspected)

	if err == nil || !strings.Contains(err.Error(), "changed while it was read at dir") {
		t.Fatalf("err = %v, want the swap refusal", err)
	}
}

func TestCopySymlinkEdgeCases(t *testing.T) {
	t.Run("an ignored symlink is not published", func(t *testing.T) {
		f := newAgentResultTest(t)
		writeAgentFile(t, f.checkout, ".gitignore", "link\n")
		if err := os.Symlink("README.md", filepath.Join(f.checkout, "link")); err != nil {
			t.Fatal(err)
		}
		writeAgentFile(t, f.checkout, "feature.txt", "real\n")
		if slices.Contains(f.trustedPaths(t, f.publish(t)), "link") {
			t.Fatal("ignored symlink published")
		}
	})
	t.Run("symlink targets count towards the byte limit", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.repository().Trusted.byteLimit = 8
		if err := os.Symlink(strings.Repeat("x", 32), filepath.Join(f.checkout, "link")); err != nil {
			t.Fatal(err)
		}
		assertRefused(t, f, "more than 8 bytes")
	})
	t.Run("a symlink that vanished is refused", func(t *testing.T) {
		f := newAgentResultTest(t)
		copier, source := newCopyHarness(t, f)
		err := copier.symlink(source, "gone", "gone")
		if err == nil || !strings.Contains(err.Error(), "changed while it was read at gone") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a symlink that cannot be created fails", func(t *testing.T) {
		f := newAgentResultTest(t)
		if err := os.Symlink("README.md", filepath.Join(f.checkout, "link")); err != nil {
			t.Fatal(err)
		}
		copier, source := newCopyHarness(t, f)
		writeAgentFile(t, copier.trusted.filesDir, "link", "occupied\n")
		if err := copier.symlink(source, "link", "link"); err == nil {
			t.Fatal("creating over an existing entry succeeded")
		}
	})
}
