package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/repo"
)

type resultUses struct {
	preview string
	check   string
	pause   string
	build   string
}

func exerciseEveryResultUse(t *testing.T, f agentResultTest) resultUses {
	t.Helper()
	ctx := context.Background()
	f.jr.job.RequiredChecks = []string{"feature-file"}
	f.jr.d.cfg.Checks = map[string]config.CheckConfig{
		"feature-file": {Command: "test", Args: []string{"-f", "feature.txt"}},
	}
	var uses resultUses
	set, err := f.jr.previewCommitSet(ctx)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	uses.preview = set["repo-1"]
	response, err := call(t, f.jr, "platform_run_check", `{"check_id":"feature-file","idempotency_key":"rc-attack"}`)
	if err != nil {
		t.Fatalf("run-check: %v", err)
	}
	result := response.(map[string]any)
	if result["result"] != "passed" || result["worktree_dirty"] != false {
		t.Fatalf("run-check result = %v", result)
	}
	uses.check = result["commit_sha"].(string)
	snapshot, err := f.jr.snapshotRepository(ctx, f.repository())
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if snapshot.PatchDigestSHA256 != "" || snapshot.Branch != f.branch {
		t.Fatalf("pause snapshot = %+v", snapshot)
	}
	uses.pause = snapshot.CommitSHA
	f.jr.job.PushBranch = true
	rm := repo.NewManager(filepath.Join(f.jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	state, extra, err := f.jr.finalizeBuild(ctx, rm, false)
	if err != nil || state != "succeeded" {
		t.Fatalf("build state=%s extra=%v err=%v", state, extra, err)
	}
	uses.build = extra["head_sha"].(string)
	if uses.preview != uses.build || uses.check != uses.build || uses.pause != uses.build {
		t.Fatalf("result uses disagree: %+v", uses)
	}
	if origin := buildWorkspaceGit(t, f.origin, "rev-parse", "refs/heads/"+f.branch); origin != uses.build {
		t.Fatalf("origin job branch = %s, want %s", origin, uses.build)
	}
	return uses
}

func assertPublishedFeature(t *testing.T, f agentResultTest, sha, content string, absent ...string) {
	t.Helper()
	assertRunnerCommit(t, f, sha, f.base)
	if got := buildWorkspaceGit(t, f.origin, "show", sha+":feature.txt"); got != content {
		t.Fatalf("published feature.txt = %q, want %q", got, content)
	}
	if got := buildWorkspaceGit(t, f.origin, "show", sha+":README.md"); got != "approved base" {
		t.Fatalf("published README.md = %q", got)
	}
	if count := buildWorkspaceGit(t, f.origin, "rev-list", "--count", f.base+".."+sha); count != "1" {
		t.Fatalf("origin received %s commits", count)
	}
	listing := buildWorkspaceGit(t, f.origin, "ls-tree", "-r", sha)
	if strings.Contains(listing, gitlinkMode+" ") || strings.Contains(listing, "shortstack-artifacts") {
		t.Fatalf("published tree = %s", listing)
	}
	for _, path := range absent {
		if strings.Contains(listing, path) {
			t.Fatalf("published tree contains %s: %s", path, listing)
		}
	}
	patch, err := os.ReadFile(filepath.Join(f.jr.workDir, "changes-repo-1-"+sha[:8]+".patch"))
	if err != nil || !strings.Contains(string(patch), "+"+content) {
		t.Fatalf("patch evidence = %q err=%v", patch, err)
	}
}

func plantAgentPrograms(t *testing.T, f agentResultTest, gitDir, worktreeAdmin string) []string {
	t.Helper()
	programs := t.TempDir()
	var markers []string
	script := func(name string) string {
		path, marker := plantMarkerProgram(t, programs, name)
		markers = append(markers, marker)
		return path
	}
	hooks := filepath.Join(programs, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{hooks, filepath.Join(gitDir, "hooks")} {
		for _, hook := range []string{"pre-commit", "post-commit", "post-checkout", "reference-transaction", "pre-push", "post-index-change", "pre-auto-gc"} {
			source := script("hook-" + filepath.Base(filepath.Dir(dir)) + "-" + hook)
			content, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, hook), content, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	settings := [][2]string{
		{"core.hooksPath", hooks},
		{"core.fsmonitor", script("fsmonitor")},
		{"filter.evil.clean", script("filter-clean")},
		{"filter.evil.smudge", script("filter-smudge")},
		{"filter.evil.process", script("filter-process")},
		{"filter.evil.required", "true"},
		{"diff.evil.textconv", script("textconv")},
		{"diff.evil.command", script("diff-command")},
		{"diff.external", script("diff-external")},
		{"core.pager", script("pager")},
		{"core.sshCommand", script("ssh")},
		{"core.askPass", script("askpass")},
		{"credential.helper", "!" + script("credential")},
		{"gpg.program", script("gpg")},
		{"commit.gpgSign", "true"},
		{"core.untrackedCache", "true"},
		{"extensions.worktreeConfig", "true"},
	}
	for _, setting := range settings {
		agentGit(t, f.checkout, "config", setting[0], setting[1])
	}
	if worktreeAdmin != "" {
		worktreeConfig := "[core]\n\tfsmonitor = " + script("worktree-fsmonitor") + "\n\thooksPath = " + hooks + "\n[diff \"evil\"]\n\ttextconv = " + script("worktree-textconv") + "\n"
		if err := os.WriteFile(filepath.Join(worktreeAdmin, "config.worktree"), []byte(worktreeConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return markers
}

func TestAttackAgentProgramsInRepositoryConfigNeverRun(t *testing.T) {
	t.Run("positive control: agent Git runs the planted programs", func(t *testing.T) {
		f := newAgentResultTest(t)
		writeAgentFile(t, f.checkout, ".gitattributes", "* filter=evil diff=evil\n")
		writeAgentFile(t, f.checkout, "feature.txt", "real")
		markers := plantAgentPrograms(t, f, filepath.Join(f.checkout, ".git"), "")
		command := exec.Command("git", "-c", "user.name=Agent", "-c", "user.email=agent@example.test", "-c", "commit.gpgSign=false", "commit", "-q", "-a", "-m", "control")
		command.Dir = f.checkout
		_ = command.Run()
		ran := 0
		for _, marker := range markers {
			if _, err := os.Stat(marker); err == nil {
				ran++
			}
		}
		if ran == 0 {
			t.Fatal("the planted programs are inert, so their absence proves nothing")
		}
	})
	t.Run("checkout", func(t *testing.T) {
		f := newAgentResultTest(t)
		writeAgentFile(t, f.checkout, ".gitattributes", "* filter=evil diff=evil\n")
		writeAgentFile(t, f.checkout, "feature.txt", "real")
		markers := plantAgentPrograms(t, f, filepath.Join(f.checkout, ".git"), "")
		uses := exerciseEveryResultUse(t, f)
		assertMarkerAbsent(t, markers...)
		assertPublishedFeature(t, f, uses.build, "real")
	})
	t.Run("worktree with config.worktree", func(t *testing.T) {
		f := newAgentResultTest(t)
		worktree := f.addWorktree(t, ".claude/worktrees/feature", "agent-feature")
		writeAgentFile(t, worktree, ".gitattributes", "* filter=evil diff=evil\n")
		agentCommit(t, worktree, "feature.txt", "real")
		markers := plantAgentPrograms(t, f, filepath.Join(f.checkout, ".git"), filepath.Join(f.checkout, ".git", "worktrees", "feature"))
		uses := exerciseEveryResultUse(t, f)
		assertMarkerAbsent(t, markers...)
		assertPublishedFeature(t, f, uses.build, "real", ".claude")
		audit, ok := lastAudit(f)
		if task, _ := audit["current_task"].(string); !ok || !strings.Contains(task, "repositories/repo-1/.claude/worktrees/feature") {
			t.Fatalf("worktree result audit = %v", audit)
		}
	})
}

func TestAttackCommitGraphPoisoningCannotChangeTheResult(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real")
	decoy := t.TempDir()
	agentGit(t, decoy, "init", "-q", "-b", "main")
	writeAgentFile(t, decoy, "decoy.txt", "decoy")
	agentGit(t, decoy, "add", ".")
	agentGit(t, decoy, "commit", "-q", "-m", "decoy")
	agentGit(t, decoy, "commit-graph", "write", "--reachable")
	graph, err := os.ReadFile(filepath.Join(decoy, ".git", "objects", "info", "commit-graph"))
	if err != nil {
		t.Fatal(err)
	}
	info := filepath.Join(f.checkout, ".git", "objects", "info")
	if err := os.MkdirAll(filepath.Join(info, "commit-graphs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "commit-graph"), graph, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "commit-graphs", "commit-graph-chain"), []byte(strings.Repeat("f", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentGit(t, f.checkout, "config", "core.commitGraph", "true")
	uses := exerciseEveryResultUse(t, f)
	assertPublishedFeature(t, f, uses.build, "real", "decoy.txt")
}

func TestAttackCoreWorktreeAndDiffRelativeCannotRedirectTheResult(t *testing.T) {
	f := newAgentResultTest(t)
	decoy := filepath.Join(f.jr.workDir, "decoy")
	writeAgentFile(t, decoy, "feature.txt", "decoy")
	writeAgentFile(t, decoy, "decoy-only.txt", "decoy")
	writeAgentFile(t, f.checkout, "feature.txt", "real")
	writeAgentFile(t, f.checkout, "sub/inner.txt", "inner")
	agentGit(t, f.checkout, "config", "core.worktree", decoy)
	agentGit(t, f.checkout, "config", "diff.relative", "sub")
	agentGit(t, f.checkout, "config", "status.relativePaths", "true")
	uses := exerciseEveryResultUse(t, f)
	assertPublishedFeature(t, f, uses.build, "real", "decoy-only.txt")
	if got := buildWorkspaceGit(t, f.origin, "show", uses.build+":sub/inner.txt"); got != "inner" {
		t.Fatalf("sub/inner.txt = %q", got)
	}
}

func TestAttackBrokenGitDirectoryWithAParentRepository(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(t *testing.T, gitDir string)
	}{
		{"missing HEAD", func(t *testing.T, gitDir string) {
			if err := os.Remove(filepath.Join(gitDir, "HEAD")); err != nil {
				t.Fatal(err)
			}
		}},
		{"garbage config", func(t *testing.T, gitDir string) {
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core\n\tbroken"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"git directory removed", func(t *testing.T, gitDir string) {
			if err := os.RemoveAll(gitDir); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			agentGit(t, f.jr.workDir, "init", "-q", "-b", "main")
			writeAgentFile(t, f.jr.workDir, "parent-decoy.txt", "decoy")
			agentGit(t, f.jr.workDir, "add", "parent-decoy.txt")
			agentGit(t, f.jr.workDir, "commit", "-q", "-m", "parent decoy")
			writeAgentFile(t, f.checkout, "feature.txt", "real")
			test.damage(t, filepath.Join(f.checkout, ".git"))
			uses := exerciseEveryResultUse(t, f)
			assertPublishedFeature(t, f, uses.build, "real", "parent-decoy.txt")
		})
	}
}

func TestAttackRedirectedCommondirCannotChangeTheResult(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/feature", "agent-feature")
	writeAgentFile(t, worktree, "feature.txt", "real")
	decoy := filepath.Join(f.jr.workDir, "decoy")
	agentGit(t, f.jr.workDir, "init", "-q", "-b", "main", decoy)
	writeAgentFile(t, decoy, "decoy.txt", "decoy")
	agentGit(t, decoy, "add", ".")
	agentGit(t, decoy, "commit", "-q", "-m", "decoy")
	decoyGit := filepath.Join(decoy, ".git")
	for _, path := range []string{
		filepath.Join(f.checkout, ".git", "commondir"),
		filepath.Join(f.checkout, ".git", "worktrees", "feature", "commondir"),
	} {
		if err := os.WriteFile(path, []byte(decoyGit+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	uses := exerciseEveryResultUse(t, f)
	assertPublishedFeature(t, f, uses.build, "real", "decoy.txt", ".claude")
}

func TestAttackIgnoreAllCannotHideAGitlink(t *testing.T) {
	t.Run("embedded repository is refused", func(t *testing.T) {
		f := newAgentResultTest(t)
		nested := filepath.Join(f.checkout, "vendor", "lib")
		agentGit(t, f.checkout, "init", "-q", "-b", "main", nested)
		agentCommit(t, nested, "lib.txt", "lib")
		writeAgentFile(t, f.checkout, ".gitmodules", "[submodule \"lib\"]\n\tpath = vendor/lib\n\turl = ./lib\n\tignore = all\n")
		agentGit(t, f.checkout, "config", "submodule.lib.ignore", "all")
		agentGit(t, f.checkout, "config", "diff.ignoreSubmodules", "all")
		agentGit(t, f.checkout, "add", ".gitmodules", "vendor/lib")
		agentGit(t, f.checkout, "commit", "-q", "-m", "hide gitlink")
		assertRefused(t, f, "contains a nested Git repository at vendor/lib/.git")
		if err := f.finalizeWithPush(t); err == nil {
			t.Fatal("build published an embedded repository")
		}
		if originHasBranch(t, f.origin, f.branch) {
			t.Fatal("a branch with an embedded repository reached origin")
		}
	})
	t.Run("gitlink committed by the agent without its directory", func(t *testing.T) {
		f := newAgentResultTest(t)
		agentGit(t, f.checkout, "update-index", "--add", "--cacheinfo", gitlinkMode+","+f.base+",vendor/lib")
		writeAgentFile(t, f.checkout, ".gitmodules", "[submodule \"lib\"]\n\tpath = vendor/lib\n\turl = ./lib\n\tignore = all\n")
		agentGit(t, f.checkout, "add", ".gitmodules")
		agentGit(t, f.checkout, "commit", "-q", "-m", "gitlink")
		agentGit(t, f.checkout, "config", "diff.ignoreSubmodules", "all")
		writeAgentFile(t, f.checkout, "feature.txt", "real")
		uses := exerciseEveryResultUse(t, f)
		assertPublishedFeature(t, f, uses.build, "real", "vendor/lib")
	})
}

func TestAttackGraftsShallowReplaceAndAlternatesCannotChangeTheResult(t *testing.T) {
	f := newAgentResultTest(t)
	agentCommit(t, f.checkout, "feature.txt", "real")
	gitDir := filepath.Join(f.checkout, ".git")
	head := agentGit(t, f.checkout, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(gitDir, "info", "grafts"), []byte(head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "shallow"), []byte(head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	agentGit(t, decoy, "init", "-q", "-b", "main")
	writeAgentFile(t, decoy, "decoy.txt", "decoy")
	agentGit(t, decoy, "add", ".")
	agentGit(t, decoy, "commit", "-q", "-m", "decoy")
	if err := os.WriteFile(filepath.Join(gitDir, "objects", "info", "alternates"), []byte(filepath.Join(decoy, ".git", "objects")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decoyHead := agentGit(t, decoy, "rev-parse", "HEAD")
	agentGit(t, f.checkout, "replace", "-f", f.base, decoyHead)
	uses := exerciseEveryResultUse(t, f)
	assertPublishedFeature(t, f, uses.build, "real", "decoy.txt")
	if parents := buildWorkspaceGit(t, f.origin, "rev-list", "--parents", "-n", "1", uses.build); parents != uses.build+" "+f.base {
		t.Fatalf("origin parents = %s", parents)
	}
}

func TestAttackAgentHistoryNeverReachesOrigin(t *testing.T) {
	f := newAgentResultTest(t)
	agentCommit(t, f.checkout, "secret.txt", "committed then removed")
	agentGit(t, f.checkout, "rm", "-q", "secret.txt")
	agentCommit(t, f.checkout, "feature.txt", "real")
	agentHead := agentGit(t, f.checkout, "rev-parse", "HEAD")
	uses := exerciseEveryResultUse(t, f)
	assertPublishedFeature(t, f, uses.build, "real", "secret.txt")
	if _, err := trustedGitTry(filepath.Join(f.origin, ".git"), "cat-file", "-e", agentHead+"^{commit}"); err == nil {
		t.Fatal("an agent commit reached origin")
	}
	rm := repo.NewManager(filepath.Join(f.jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if err := f.jr.pushBranch(context.Background(), f.repository(), rm, agentHead); err == nil {
		t.Fatal("push of an agent commit was accepted")
	}
	if origin := buildWorkspaceGit(t, f.origin, "rev-parse", "refs/heads/"+f.branch); origin != uses.build {
		t.Fatalf("origin job branch moved to %s", origin)
	}
}
