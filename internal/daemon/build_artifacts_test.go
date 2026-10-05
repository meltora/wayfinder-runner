package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/repo"
	"golang.org/x/text/unicode/norm"
)

func TestPrepareWorkspaceExcludesRunnerArtifactsBeforeTheAgentStarts(t *testing.T) {
	jr, _, _ := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	exclude, err := os.ReadFile(filepath.Join(workDir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count("\n"+string(exclude), "\n"+runnerArtifactsExcludeEntry+"\n") != 1 {
		t.Fatalf("exclude file lacks one runner entry:\n%s", exclude)
	}
	if err := ensureRunnerArtifactsExcluded(workDir); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(workDir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(exclude) {
		t.Fatalf("exclude entry is not idempotent:\n%s", again)
	}
	writeFixtureFile(t, workDir, "shortstack-artifacts/coverage-summary.json", "{}", 0o644)
	if status := buildWorkspaceGit(t, workDir, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("artifact is visible to git status: %q", status)
	}
}

func TestLeftoverArtifactIsAbsentFromThePushedCommit(t *testing.T) {
	jr, origin, authorizedSHA := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	writeFixtureFile(t, workDir, "implemented.txt", "approved change\n", 0o644)
	writeFixtureFile(t, workDir, "shortstack-artifacts/coverage-summary.json", "{}", 0o644)
	writeFixtureFile(t, workDir, "nested/shortstack-artifacts/report.json", "{}", 0o644)
	writeFixtureFile(t, workDir, ".git/info/exclude", "", 0o644)

	sha, err := jr.commitAgentChanges(context.Background(), jr.repositories[0])
	if err != nil {
		t.Fatal(err)
	}
	if parent := buildWorkspaceGit(t, workDir, "rev-parse", sha+"^"); parent != authorizedSHA {
		t.Fatalf("commit parent = %s", parent)
	}
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if err := jr.pushBranch(context.Background(), jr.repositories[0], rm, sha); err != nil {
		t.Fatal(err)
	}
	pushed := buildWorkspaceGit(t, origin, "ls-tree", "-r", "--name-only", jr.repositories[0].Branch)
	if !strings.Contains(pushed, "implemented.txt") {
		t.Fatalf("pushed tree lacks the agent change:\n%s", pushed)
	}
	if strings.Contains(pushed, "shortstack-artifacts") {
		t.Fatalf("pushed tree contains a runner artifact:\n%s", pushed)
	}
}

func TestCommitWithOnlyLeftoverArtifactsCreatesNoCommit(t *testing.T) {
	jr, _, authorizedSHA := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	writeFixtureFile(t, workDir, ".git/info/exclude", "", 0o644)
	writeFixtureFile(t, workDir, "shortstack-artifacts/coverage-summary.json", "{}", 0o644)
	sha, err := jr.commitAgentChanges(context.Background(), jr.repositories[0])
	if err != nil {
		t.Fatal(err)
	}
	if sha != authorizedSHA {
		t.Fatalf("head = %s, want unchanged %s", sha, authorizedSHA)
	}
}

func TestCommitRefusesAnArtifactTheAgentCommittedItself(t *testing.T) {
	jr, _, _ := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	writeFixtureFile(t, workDir, "shortstack-artifacts/coverage-summary.json", "{}", 0o644)
	buildWorkspaceGit(t, workDir, "add", "-f", "shortstack-artifacts/coverage-summary.json")
	buildWorkspaceGit(t, workDir, "-c", "user.name=agent", "-c", "user.email=agent@example.test", "commit", "-m", "agent commit")
	if _, err := jr.commitAgentChanges(context.Background(), jr.repositories[0]); err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
		t.Fatalf("committed artifact accepted: %v", err)
	}
}

func commitThenDeleteArtifact(t *testing.T, workDir, artifact string) {
	t.Helper()
	writeFixtureFile(t, workDir, artifact, "{\"secret\":1}", 0o644)
	buildWorkspaceGit(t, workDir, "add", "-f", artifact)
	buildWorkspaceGit(t, workDir, "-c", "user.name=agent", "-c", "user.email=agent@example.test", "commit", "-m", "agent commit 1")
	buildWorkspaceGit(t, workDir, "rm", "-q", artifact)
	writeFixtureFile(t, workDir, "implemented.txt", "approved change\n", 0o644)
	buildWorkspaceGit(t, workDir, "add", "implemented.txt")
	buildWorkspaceGit(t, workDir, "-c", "user.name=agent", "-c", "user.email=agent@example.test", "commit", "-m", "agent commit 2")
}

func originHasBranch(t *testing.T, origin, branch string) bool {
	t.Helper()
	command := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	command.Dir = origin
	return command.Run() == nil
}

func TestArtifactCommittedThenDeletedIsNeverPushed(t *testing.T) {
	for _, artifact := range []string{
		"shortstack-artifacts/coverage-summary.json",
		"deep/x/shortstack-artifacts/b/c.json",
	} {
		for _, publishReviewReady := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/review-ready=%t", artifact, publishReviewReady), func(t *testing.T) {
				jr, origin, _ := pinnedBuildWorkspaceFixture(t)
				jr.job.PushBranch = true
				workDir, cleanup, err := jr.prepareWorkspace(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(cleanup)
				commitThenDeleteArtifact(t, workDir, artifact)
				rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
				rm.AllowLocal = true
				state, _, err := jr.finalizeBuild(context.Background(), rm, publishReviewReady)
				if err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
					t.Fatalf("finalize state=%s err=%v, want artifact refusal", state, err)
				}
				if originHasBranch(t, origin, jr.repositories[0].Branch) {
					t.Fatalf("branch with artifact history reached the origin:\n%s",
						buildWorkspaceGit(t, origin, "log", "--name-only", "--format=%s", jr.repositories[0].Branch))
				}
			})
		}
	}
}

func TestPushBranchRefusesArtifactAnywhereInTheBranchHistory(t *testing.T) {
	for _, artifact := range []string{
		"shortstack-artifacts/coverage-summary.json",
		"deep/x/shortstack-artifacts/b/c.json",
	} {
		t.Run(artifact, func(t *testing.T) {
			jr, origin, _ := pinnedBuildWorkspaceFixture(t)
			workDir, cleanup, err := jr.prepareWorkspace(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			commitThenDeleteArtifact(t, workDir, artifact)
			sha := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD")
			rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
			rm.AllowLocal = true
			err = jr.pushBranch(context.Background(), jr.repositories[0], rm, sha)
			if err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
				t.Fatalf("push err=%v, want artifact refusal", err)
			}
			if originHasBranch(t, origin, jr.repositories[0].Branch) {
				t.Fatal("branch with artifact history reached the origin")
			}
		})
	}
}

func TestArtifactLikeNamesOutsideTheDirectoryArePushed(t *testing.T) {
	jr, origin, _ := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	writeFixtureFile(t, workDir, "docs/shortstack-artifacts.md", "notes\n", 0o644)
	writeFixtureFile(t, workDir, "shortstack-artifacts-old/x.txt", "x\n", 0o644)
	sha, err := jr.commitAgentChanges(context.Background(), jr.repositories[0])
	if err != nil {
		t.Fatal(err)
	}
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if err := jr.pushBranch(context.Background(), jr.repositories[0], rm, sha); err != nil {
		t.Fatal(err)
	}
	if !originHasBranch(t, origin, jr.repositories[0].Branch) {
		t.Fatal("branch was not pushed")
	}
}

func TestArtifactIntroducedByAMergeCommitIsRefused(t *testing.T) {
	jr, origin, _ := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	agent := []string{"-c", "user.name=agent", "-c", "user.email=agent@example.test"}
	branch := jr.repositories[0].Branch
	buildWorkspaceGit(t, workDir, "checkout", "-q", "-b", "side")
	writeFixtureFile(t, workDir, "side.txt", "side\n", 0o644)
	buildWorkspaceGit(t, workDir, "add", "side.txt")
	buildWorkspaceGit(t, workDir, append(agent, "commit", "-q", "-m", "side")...)
	buildWorkspaceGit(t, workDir, "checkout", "-q", branch)
	writeFixtureFile(t, workDir, "implemented.txt", "approved change\n", 0o644)
	buildWorkspaceGit(t, workDir, "add", "implemented.txt")
	buildWorkspaceGit(t, workDir, append(agent, "commit", "-q", "-m", "main")...)
	buildWorkspaceGit(t, workDir, append(agent, "merge", "-q", "--no-commit", "side")...)
	writeFixtureFile(t, workDir, "nested/shortstack-artifacts/z.json", "{}", 0o644)
	buildWorkspaceGit(t, workDir, "add", "-f", "nested/shortstack-artifacts/z.json")
	buildWorkspaceGit(t, workDir, append(agent, "commit", "-q", "-m", "merge")...)
	buildWorkspaceGit(t, workDir, "checkout", "-q", "side")
	writeFixtureFile(t, workDir, "side-two.txt", "side two\n", 0o644)
	buildWorkspaceGit(t, workDir, "add", "side-two.txt")
	buildWorkspaceGit(t, workDir, append(agent, "commit", "-q", "-m", "side two")...)
	buildWorkspaceGit(t, workDir, "checkout", "-q", branch)
	buildWorkspaceGit(t, workDir, append(agent, "merge", "-q", "--no-commit", "side")...)
	buildWorkspaceGit(t, workDir, "rm", "-q", "nested/shortstack-artifacts/z.json")
	buildWorkspaceGit(t, workDir, append(agent, "commit", "-q", "-m", "merge two")...)
	if diff := buildWorkspaceGit(t, workDir, "diff", "--name-only", jr.repositories[0].BaseSHA, "HEAD"); strings.Contains(diff, "shortstack-artifacts") {
		t.Fatalf("fixture leaves the artifact in the net diff:\n%s", diff)
	}
	if _, err := jr.commitAgentChanges(context.Background(), jr.repositories[0]); err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
		t.Fatalf("merge-introduced artifact accepted: %v", err)
	}
	if originHasBranch(t, origin, branch) {
		t.Fatal("branch reached the origin")
	}
}

var caseFoldedRunnerArtifactDirectories = []string{
	"Shortstack-Artifacts",
	"SHORTSTACK-ARTIFACTS",
	"Shortstack-artifacts",
	"\u017fhortstack-artifacts",
	"short\ufb06ack-artifacts",
	"shortstac\u212a-artifacts",
	"short\ufb05ack-artifacts",
	"short\u017ftack-artifacts",
	"\u017fHORT\ufb06ACK-ARTIFAC\u0054\u017f",
}

func TestRunnerArtifactPathMatchesEveryCaseFoldedAndNormalizedSpelling(t *testing.T) {
	for _, directory := range caseFoldedRunnerArtifactDirectories {
		for _, path := range []string{
			directory + "/a.json",
			"nested/" + directory + "/a.json",
			"Deep/X/" + directory + "/b/c.json",
		} {
			for _, spelling := range []string{path, norm.NFC.String(path), norm.NFD.String(path)} {
				if !runnerArtifactPath(spelling) {
					t.Errorf("runner artifact path %+q is not recognized", spelling)
				}
			}
		}
	}
}

func TestRunnerArtifactPathAcceptsInnocuousNames(t *testing.T) {
	for _, path := range []string{
		"",
		"README.md",
		"docs/shortstack-artifacts.md",
		"Docs/Shortstack-Artifacts.md",
		"shortstack-artifacts-old/x.txt",
		"SHORTSTACK-ARTIFACTS-OLD/x.txt",
		"shortstack_artifacts/x.txt",
		"shortstackartifacts/x.txt",
		"shortstack-artifact/x.txt",
		"x/shortstack-artifacts.d/y.txt",
		"\uff53hortstack-artifacts/x.txt",
		"shortstack\u200d-artifacts/x.txt",
		"shortstack\u00ad-artifacts/x.txt",
		"shortstack\u2010artifacts/x.txt",
		"shortstack-artifacts\u0301/x.txt",
		"shortstack-artifact\u015b/x.txt",
		norm.NFD.String("shortstack-artifact\u015b/x.txt"),
	} {
		if runnerArtifactPath(path) {
			t.Errorf("innocuous path %+q is treated as a runner artifact", path)
		}
	}
}

func TestCaseFoldedArtifactSpellingsAreNeverCommittedOrPushed(t *testing.T) {
	for _, directory := range caseFoldedRunnerArtifactDirectories {
		for _, parent := range []string{"", "nested/"} {
			for _, publishReviewReady := range []bool{false, true} {
				artifact := parent + directory + "/coverage-summary.json"
				t.Run(fmt.Sprintf("%+q/review-ready=%t", artifact, publishReviewReady), func(t *testing.T) {
					jr, origin, _ := pinnedBuildWorkspaceFixture(t)
					jr.job.PushBranch = true
					workDir, cleanup, err := jr.prepareWorkspace(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(cleanup)
					writeFixtureFile(t, workDir, parent+"shortstack-artifacts/coverage-summary.json", "{}", 0o644)
					writeFixtureFile(t, workDir, artifact, "{\"secret\":1}", 0o644)
					buildWorkspaceGit(t, workDir, "add", "-f", artifact)
					buildWorkspaceGit(t, workDir, "-c", "user.name=agent", "-c", "user.email=agent@example.test", "commit", "-q", "-m", "agent commit")
					rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
					rm.AllowLocal = true
					state, _, err := jr.finalizeBuild(context.Background(), rm, publishReviewReady)
					if err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
						t.Fatalf("finalize state=%s err=%v, want artifact refusal", state, err)
					}
					if originHasBranch(t, origin, jr.repositories[0].Branch) {
						t.Fatalf("branch with a case-folded artifact reached the origin:\n%s",
							buildWorkspaceGit(t, origin, "log", "--name-only", "--format=%s", jr.repositories[0].Branch))
					}
				})
			}
		}
	}
}

func TestInnocuousUnicodeNamesArePushed(t *testing.T) {
	jr, origin, _ := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, path := range []string{
		"Docs/Shortstack-Artifacts.md",
		"SHORTSTACK-ARTIFACTS-OLD/x.txt",
		"\uff53hortstack-artifacts/x.txt",
		"shortstack\u200d-artifacts/x.txt",
		"shortstack-artifacts\u0301/x.txt",
	} {
		writeFixtureFile(t, workDir, path, "x\n", 0o644)
	}
	sha, err := jr.commitAgentChanges(context.Background(), jr.repositories[0])
	if err != nil {
		t.Fatal(err)
	}
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if err := jr.pushBranch(context.Background(), jr.repositories[0], rm, sha); err != nil {
		t.Fatal(err)
	}
	if !originHasBranch(t, origin, jr.repositories[0].Branch) {
		t.Fatal("branch was not pushed")
	}
}

func TestReplaceRefCannotHideAnArtifactCommit(t *testing.T) {
	jr, origin, base := pinnedBuildWorkspaceFixture(t)
	jr.job.PushBranch = true
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	commitThenDeleteArtifact(t, workDir, "shortstack-artifacts/coverage-summary.json")
	head := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD")
	buildWorkspaceGit(t, workDir, "-c", "user.name=agent", "-c", "user.email=agent@example.test", "replace", "--graft", head, base)
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if state, _, err := jr.finalizeBuild(context.Background(), rm, false); err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
		t.Fatalf("finalize state=%s err=%v, want artifact refusal", state, err)
	}
	if originHasBranch(t, origin, jr.repositories[0].Branch) {
		t.Fatal("branch with an artifact hidden by a replace ref reached the origin")
	}
}

func TestArtifactAddedAndRemovedOnAMergedSideBranchIsRefused(t *testing.T) {
	jr, origin, _ := pinnedBuildWorkspaceFixture(t)
	jr.job.PushBranch = true
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	agent := []string{"-c", "user.name=agent", "-c", "user.email=agent@example.test"}
	branch := jr.repositories[0].Branch
	buildWorkspaceGit(t, workDir, "checkout", "-q", "-b", "side")
	commitThenDeleteArtifact(t, workDir, "shortstack-artifacts/coverage-summary.json")
	buildWorkspaceGit(t, workDir, "checkout", "-q", branch)
	writeFixtureFile(t, workDir, "main.txt", "main\n", 0o644)
	buildWorkspaceGit(t, workDir, "add", "main.txt")
	buildWorkspaceGit(t, workDir, append(agent, "commit", "-q", "-m", "main")...)
	buildWorkspaceGit(t, workDir, append(agent, "merge", "-q", "--no-edit", "side")...)
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if state, _, err := jr.finalizeBuild(context.Background(), rm, false); err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
		t.Fatalf("finalize state=%s err=%v, want artifact refusal", state, err)
	}
	if originHasBranch(t, origin, branch) {
		t.Fatal("branch with a side-branch artifact reached the origin")
	}
}

func TestPushBranchChecksThePushedCommitRatherThanHEAD(t *testing.T) {
	jr, origin, base := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	commitThenDeleteArtifact(t, workDir, "shortstack-artifacts/coverage-summary.json")
	sha := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD")
	buildWorkspaceGit(t, workDir, "checkout", "-q", "--detach", base)
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if err := jr.pushBranch(context.Background(), jr.repositories[0], rm, sha); err == nil || !strings.Contains(err.Error(), "shortstack-artifacts") {
		t.Fatalf("push err=%v, want artifact refusal", err)
	}
	if originHasBranch(t, origin, jr.repositories[0].Branch) {
		t.Fatal("artifact-bearing commit reached the origin while HEAD was clean")
	}
}

func TestEmptyBaseSHAFailsClosedBeforeTheArtifactCheck(t *testing.T) {
	jr, origin, _ := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	commitThenDeleteArtifact(t, workDir, "shortstack-artifacts/coverage-summary.json")
	sha := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD")
	repository := jr.repositories[0]
	repository.BaseSHA = ""
	if err := jr.refuseCommittedRunnerArtifacts(context.Background(), repository, sha); err == nil {
		t.Fatal("artifact check passed without a base commit")
	}
	if _, err := jr.commitAgentChanges(context.Background(), repository); err == nil {
		t.Fatal("commit accepted without a base commit")
	}
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if err := jr.pushBranch(context.Background(), repository, rm, sha); err == nil {
		t.Fatal("push accepted without a base commit")
	}
	if originHasBranch(t, origin, repository.Branch) {
		t.Fatal("branch reached the origin without a base commit")
	}
}

func TestSymbolicBaseSHAFailsClosedBeforeTheArtifactCheck(t *testing.T) {
	jr, origin, base := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	buildWorkspaceGit(t, workDir, "branch", "-f", "main", base)
	writeFixtureFile(t, workDir, "feature.txt", "feature\n", 0o644)
	buildWorkspaceGit(t, workDir, "add", "feature.txt")
	buildWorkspaceGit(t, workDir, "-c", "user.name=Agent", "-c", "user.email=agent@example.test", "commit", "-q", "-m", "feature")
	sha := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD")
	repository := jr.repositories[0]
	repository.BaseSHA = "main"
	want := "artifact_check_base_sha must be a full lowercase Git commit SHA"
	if err := jr.refuseCommittedRunnerArtifacts(context.Background(), repository, sha); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("artifact check err=%v, want %q", err, want)
	}
	if _, err := jr.commitAgentChanges(context.Background(), repository); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("commit err=%v, want %q", err, want)
	}
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	if err := jr.pushBranch(context.Background(), repository, rm, sha); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("push err=%v, want %q", err, want)
	}
	if originHasBranch(t, origin, repository.Branch) {
		t.Fatal("branch reached the origin with a symbolic base")
	}
}
