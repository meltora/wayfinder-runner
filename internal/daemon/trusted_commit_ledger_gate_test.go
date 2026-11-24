package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/repo"
)

func TestPreviewCommitSetResolutionAloneRecordsNothing(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "resolved only\n")
	resolved, err := f.jr.previewCommitSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ledgerTrusts(t, f.jr, resolved) {
		t.Fatal("resolution without a publication reached the trusted ledger")
	}
}

func TestMidBuildPreviewOfAGitlinkCommitIsNeverTrusted(t *testing.T) {
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
	writeAgentFile(t, f.checkout, "feature.txt", "gitlink preview\n")

	resolved := publishPreviewAndResolve(t, f, "preview-gitlink")

	if err := refuseCommittedGitlinks(context.Background(), *repository, resolved["repo-1"]); err == nil {
		t.Fatal("fixture did not produce a gitlink result")
	}
	if ledgerTrusts(t, f.jr, resolved) {
		t.Fatal("a commit adding a gitlink became deploy-trusted")
	}
}

func TestMidBuildPreviewOfARunnerArtifactCommitIsNeverTrusted(t *testing.T) {
	f := newAgentResultTest(t)
	baseTree := strings.TrimSpace(f.trustedGit(t, "rev-parse", f.base+"^{tree}"))
	crafted := trustedCommitWithEntry(t, f, f.base, "040000 tree "+baseTree+"\t"+runnerArtifactsDirectory)
	repository := &f.jr.repositories[0]
	repository.StartSHA = crafted
	writeAgentFile(t, f.checkout, "feature.txt", "artifact preview\n")

	resolved := publishPreviewAndResolve(t, f, "preview-artifact")

	if err := f.jr.refuseCommittedRunnerArtifacts(context.Background(), *repository, resolved["repo-1"]); err == nil {
		t.Fatal("fixture did not produce a runner artifact result")
	}
	if ledgerTrusts(t, f.jr, resolved) {
		t.Fatal("a commit carrying runner artifacts became deploy-trusted")
	}
}

func TestTrustedCommitSetIsNotRecordedAfterLeaseLoss(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "stale attempt\n")
	resolved, err := f.jr.previewCommitSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.jr.d.leases.Release(f.jr.job.AttemptID)

	f.jr.recordTrustedCommitSet(context.Background(), resolved)

	if ledgerTrusts(t, f.jr, resolved) {
		t.Fatal("a fenced-off attempt recorded a trusted commit set")
	}
}

func TestBuildAfterLeaseLossRecordsNoTrustedCommitSet(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "lost lease\n")
	f.jr.job.PushBranch = false
	f.jr.d.leases.Release(f.jr.job.AttemptID)
	rm := repo.NewManager(filepath.Join(f.jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true

	_, extra, _ := f.jr.finalizeBuild(context.Background(), rm, false)

	commitSet, ok := extra["commit_set"].(map[string]string)
	if !ok {
		t.Fatalf("build did not resolve a commit set: %v", extra)
	}
	if ledgerTrusts(t, f.jr, commitSet) {
		t.Fatal("a build without a valid lease recorded a trusted commit set")
	}
}

func TestTrustedCommitSetMustCoverEveryRepository(t *testing.T) {
	f := newAgentResultTest(t)
	resolved, err := f.jr.previewCommitSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.jr.trustedCommitSetEligible(context.Background(), map[string]string{}); err == nil {
		t.Fatal("an empty commit set was eligible")
	}
	if err := f.jr.trustedCommitSetEligible(context.Background(), map[string]string{"other": resolved["repo-1"]}); err == nil {
		t.Fatal("a commit set for an unknown repository was eligible")
	}
	if err := f.jr.trustedCommitSetEligible(context.Background(), map[string]string{"repo-1": resolved["repo-1"], "other": resolved["repo-1"]}); err == nil {
		t.Fatal("a commit set with an extra repository was eligible")
	}
	if err := f.jr.trustedCommitSetEligible(context.Background(), resolved); err != nil {
		t.Fatalf("the resolved set was refused: %v", err)
	}
}
