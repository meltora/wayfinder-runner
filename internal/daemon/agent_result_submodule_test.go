package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const declaredSubmodule = "[submodule \"sub\"]\n\tpath = libs/sub\n\turl = https://example.test/sub.git\n"

func newSubmoduleResultTest(t *testing.T) (agentResultTest, string) {
	t.Helper()
	var pointer string
	f := newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, ".gitmodules", declaredSubmodule)
		pointer = seedGitlink(t, origin)
	})
	return f, pointer
}

func initAgentSubmodule(t *testing.T, f agentResultTest) (string, string) {
	t.Helper()
	dir := filepath.Join(f.checkout, "libs", "sub")
	agentGit(t, f.checkout, "init", "-q", "-b", "main", dir)
	head := agentCommit(t, dir, "lib.txt", "lib")
	return dir, head
}

func initSeparateSubmodule(t *testing.T, f agentResultTest) string {
	t.Helper()
	dir := filepath.Join(f.checkout, "libs", "sub")
	modules := filepath.Join(f.checkout, ".git", "modules")
	if err := os.MkdirAll(modules, 0o700); err != nil {
		t.Fatal(err)
	}
	agentGit(t, f.checkout, "init", "-q", "-b", "main", "--separate-git-dir", filepath.Join(modules, "sub"), dir)
	return dir
}

func assertGitlink(t *testing.T, f agentResultTest, commit, want string) {
	t.Helper()
	if mode, object := treeEntry(t, f, commit, "libs/sub"); mode != gitlinkMode || object != want {
		t.Fatalf("gitlink = %s %s, want %s", mode, object, want)
	}
}

func TestSubmodulePointerChangesArePublished(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f agentResultTest) string
	}{
		{"a branch in a loose ref", func(t *testing.T, f agentResultTest) string {
			_, head := initAgentSubmodule(t, f)
			return head
		}},
		{"a branch in packed refs", func(t *testing.T, f agentResultTest) string {
			dir, head := initAgentSubmodule(t, f)
			agentGit(t, dir, "pack-refs", "--all")
			if _, err := os.Lstat(filepath.Join(dir, ".git", "refs", "heads", "main")); err == nil {
				t.Fatal("the loose ref remains, so this test proves nothing")
			}
			return head
		}},
		{"a detached head", func(t *testing.T, f agentResultTest) string {
			dir, head := initAgentSubmodule(t, f)
			agentGit(t, dir, "checkout", "-q", "--detach")
			return head
		}},
		{"an absolute gitfile inside the workspace", func(t *testing.T, f agentResultTest) string {
			dir := initSeparateSubmodule(t, f)
			return agentCommit(t, dir, "lib.txt", "lib")
		}},
		{"a relative gitfile inside the workspace", func(t *testing.T, f agentResultTest) string {
			dir := initSeparateSubmodule(t, f)
			head := agentCommit(t, dir, "lib.txt", "lib")
			writeAgentFile(t, dir, ".git", "gitdir: ../../.git/modules/sub\n")
			return head
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newSubmoduleResultTest(t)
			want := tc.prepare(t, f)

			assertGitlink(t, f, f.publish(t), want)
		})
	}
}

func TestSubmodulePointerChangeReachesTheBranch(t *testing.T) {
	f, _ := newSubmoduleResultTest(t)
	_, head := initAgentSubmodule(t, f)

	if err := f.finalizeWithPush(t); err != nil {
		t.Fatal(err)
	}

	line := strings.Fields(trustedGitForTest(t, filepath.Join(f.origin, ".git"), "ls-tree", "refs/heads/"+f.branch, "--", "libs/sub"))
	if len(line) < 3 || line[0] != gitlinkMode || line[2] != head {
		t.Fatalf("origin gitlink = %v, want %s", line, head)
	}
}

func TestSubmodulePointerIsKeptWhenUnchangedOrUnpopulated(t *testing.T) {
	t.Run("unpopulated", func(t *testing.T) {
		f, pointer := newSubmoduleResultTest(t)
		writeAgentFile(t, f.checkout, "feature.txt", "real\n")
		assertGitlink(t, f, f.publish(t), pointer)
	})
	t.Run("a head equal to the start pointer", func(t *testing.T) {
		f, pointer := newSubmoduleResultTest(t)
		writeAgentFile(t, f.checkout, "libs/sub/.git/HEAD", pointer+"\n")
		writeAgentFile(t, f.checkout, "feature.txt", "real\n")
		assertGitlink(t, f, f.publish(t), pointer)
	})
	t.Run("an undeclared gitlink keeps its start pointer", func(t *testing.T) {
		var pointer string
		f := newAgentResultTestWith(t, func(origin string) { pointer = seedGitlink(t, origin) })
		initAgentSubmodule(t, f)
		writeAgentFile(t, f.checkout, "feature.txt", "real\n")
		assertGitlink(t, f, f.publish(t), pointer)
	})
}

func TestSubmodulePointerRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f agentResultTest)
		want    string
	}{
		{"an invalid head", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, "libs/sub/.git/HEAD", "not a commit\n")
		}, "submodule libs/sub has a HEAD the runner cannot read"},
		{"an unborn branch", func(t *testing.T, f agentResultTest) {
			agentGit(t, f.checkout, "init", "-q", "-b", "main", filepath.Join(f.checkout, "libs", "sub"))
		}, "submodule libs/sub has a HEAD the runner cannot read"},
		{"a gitfile outside the workspace", func(t *testing.T, f agentResultTest) {
			outside := t.TempDir()
			writeAgentFile(t, outside, "HEAD", strings.Repeat("b", 40)+"\n")
			writeAgentFile(t, f.checkout, "libs/sub/.git", "gitdir: "+outside+"\n")
		}, "submodule libs/sub has Git metadata outside the job workspace"},
		{"a gitfile reached through a symbolic link", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, ".git/modules/real/HEAD", strings.Repeat("b", 40)+"\n")
			if err := os.Symlink("real", filepath.Join(f.checkout, ".git", "modules", "alias")); err != nil {
				t.Fatal(err)
			}
			writeAgentFile(t, f.checkout, "libs/sub/.git", "gitdir: ../../.git/modules/alias\n")
		}, "submodule libs/sub has a HEAD the runner cannot read"},
		{"a symbolic link as .git", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, "elsewhere/HEAD", strings.Repeat("b", 40)+"\n")
			if err := os.MkdirAll(filepath.Join(f.checkout, "libs", "sub"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../elsewhere", filepath.Join(f.checkout, "libs", "sub", ".git")); err != nil {
				t.Fatal(err)
			}
		}, "submodule libs/sub has a .git that is neither a directory nor a gitdir file"},
		{"a shared Git directory with a branch head", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, "libs/sub/.git/HEAD", "ref: refs/heads/main\n")
			writeAgentFile(t, f.checkout, "libs/sub/.git/commondir", "../../../.git\n")
		}, "submodule libs/sub uses a shared Git directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newSubmoduleResultTest(t)
			tc.prepare(t, f)
			assertRefused(t, f, tc.want)
			_, err := f.jr.resolveAgentResult(context.Background(), f.repository(), "check run")
			if err != nil {
				assertNoHostPath(t, err.Error(), f.jr.d.cfg.StateDir, f.checkout)
			}
		})
	}
}

func TestDeclaredSubmodulesComeFromTheStartCommit(t *testing.T) {
	f, _ := newSubmoduleResultTest(t)
	trusted := f.repository().Trusted
	if !trusted.declaredSubmodules["libs/sub"] {
		t.Fatalf("declared submodules = %v", trusted.declaredSubmodules)
	}
	writeAgentFile(t, f.checkout, ".gitmodules", "[submodule \"other\"]\n\tpath = other\n\turl = ./other\n")
	if len(trusted.declaredSubmodules) != 1 {
		t.Fatalf("declared submodules = %v", trusted.declaredSubmodules)
	}
	plain := newAgentResultTest(t)
	declared, err := plain.repository().Trusted.submodulePaths(context.Background(), plain.base)
	if err != nil || len(declared) != 0 {
		t.Fatalf("a commit without .gitmodules declared %v (err %v)", declared, err)
	}
}
