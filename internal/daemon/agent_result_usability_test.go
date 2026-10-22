package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func agentGitWithHome(t *testing.T, dir, home string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=Agent", "-c", "user.email=agent@example.test"}, args...)...)
	command.Dir = dir
	command.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func TestAgentRepositoryConfigNoLongerAffectsPublication(t *testing.T) {
	seed := func(origin string) {
		writeAgentFile(t, origin, "src/app.txt", "app\n")
		writeAgentFile(t, origin, "docs/guide.txt", "guide\n")
	}
	for _, test := range []struct {
		name   string
		act    func(t *testing.T, f agentResultTest)
		assert func(t *testing.T, f agentResultTest, sha string)
	}{
		{
			name: "husky core.hooksPath",
			act: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, ".husky/pre-commit", "#!/bin/sh\nexit 0\n")
				if err := os.Chmod(filepath.Join(f.checkout, ".husky", "pre-commit"), 0o700); err != nil {
					t.Fatal(err)
				}
				agentGit(t, f.checkout, "config", "core.hooksPath", ".husky")
				agentCommit(t, f.checkout, "feature.txt", "real")
			},
			assert: func(t *testing.T, f agentResultTest, sha string) {
				if mode, _ := treeEntry(t, f, sha, ".husky/pre-commit"); mode != "100755" {
					t.Fatalf(".husky/pre-commit mode = %q", mode)
				}
			},
		},
		{
			name: "git remote add",
			act: func(t *testing.T, f agentResultTest) {
				agentGit(t, f.checkout, "remote", "add", "upstream", "https://example.invalid/upstream.git")
				writeAgentFile(t, f.checkout, "feature.txt", "real")
			},
		},
		{
			name: "git sparse-checkout set",
			act: func(t *testing.T, f agentResultTest) {
				agentGit(t, f.checkout, "sparse-checkout", "set", "--cone", "src")
				if _, err := os.Stat(filepath.Join(f.checkout, "docs", "guide.txt")); !os.IsNotExist(err) {
					t.Fatalf("sparse checkout kept an out-of-cone file: %v", err)
				}
				writeAgentFile(t, f.checkout, "src/app.txt", "app changed\n")
				writeAgentFile(t, f.checkout, "feature.txt", "real")
			},
			assert: func(t *testing.T, f agentResultTest, sha string) {
				if got := f.trustedGit(t, "show", sha+":docs/guide.txt"); got != "guide" {
					t.Fatalf("out-of-cone file = %q", got)
				}
				if got := f.trustedGit(t, "show", sha+":src/app.txt"); got != "app changed" {
					t.Fatalf("in-cone change = %q", got)
				}
			},
		},
		{
			name: "git maintenance register",
			act: func(t *testing.T, f agentResultTest) {
				agentGitWithHome(t, f.checkout, t.TempDir(), "maintenance", "register")
				writeAgentFile(t, f.checkout, "feature.txt", "real")
			},
		},
		{
			name: "pull.rebase and worktree.useRelativePaths",
			act: func(t *testing.T, f agentResultTest) {
				agentGit(t, f.checkout, "config", "pull.rebase", "true")
				agentGit(t, f.checkout, "config", "worktree.useRelativePaths", "true")
				worktree := f.addWorktree(t, ".claude/worktrees/feature", "agent-feature")
				agentCommit(t, worktree, "feature.txt", "real")
			},
		},
		{
			name: "other repository config",
			act: func(t *testing.T, f agentResultTest) {
				for _, setting := range [][2]string{
					{"core.autocrlf", "true"},
					{"core.ignoreCase", "true"},
					{"core.symlinks", "false"},
					{"core.excludesFile", filepath.Join(t.TempDir(), "missing")},
					{"user.email", "someone@example.test"},
					{"extensions.worktreeConfig", "true"},
					{"index.version", "4"},
					{"feature.manyFiles", "true"},
				} {
					agentGit(t, f.checkout, "config", setting[0], setting[1])
				}
				writeAgentFile(t, f.checkout, "feature.txt", "real")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAgentResultTestWith(t, seed)
			test.act(t, f)
			uses := exerciseEveryResultUse(t, f)
			assertPublishedFeature(t, f, uses.build, "real", ".claude")
			for _, path := range []string{"src/app.txt", "docs/guide.txt"} {
				if mode, _ := treeEntry(t, f, uses.build, path); mode != "100644" {
					t.Fatalf("%s missing from the result (mode %q)", path, mode)
				}
			}
			if test.assert != nil {
				test.assert(t, f, uses.build)
			}
		})
	}
}
