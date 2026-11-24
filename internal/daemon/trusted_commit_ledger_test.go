package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/repo"
)

func ledgerTrusts(t *testing.T, jr *jobRunner, commitSet map[string]string) bool {
	t.Helper()
	commits := make([]v1.PreviewObservationCommit, 0, len(commitSet))
	for repositoryID, sha := range commitSet {
		commits = append(commits, v1.PreviewObservationCommit{RepositoryID: repositoryID, CommitSHA: sha})
	}
	operation := v1.PreviewDeployOperation{
		PreviewDeployOperationAuthority: v1.PreviewDeployOperationAuthority{
			ExecutionManifestID: jr.job.ManifestID, ExecutionManifestDigest: jr.job.ManifestDigest,
		},
		CommitSet: commits,
	}
	slices.SortFunc(operation.CommitSet, func(left, right v1.PreviewObservationCommit) int {
		return strings.Compare(left.RepositoryID, right.RepositoryID)
	})
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	trusted, err := jr.d.previewDeployCommitSetTrusted(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	return trusted
}

func TestBuildWithoutPushRecordsTrustedCommitSet(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "unpushed\n")
	f.jr.job.PushBranch = false
	rm := repo.NewManager(filepath.Join(f.jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	state, extra, err := f.jr.finalizeBuild(context.Background(), rm, false)
	if err != nil || state != "succeeded" || extra["pushed"] != nil {
		t.Fatalf("state=%s extra=%v err=%v", state, extra, err)
	}
	if !ledgerTrusts(t, f.jr, extra["commit_set"].(map[string]string)) {
		t.Fatal("build without push did not record its resolved commit set")
	}
}

func TestBuildWithFailedRequiredChecksStillRecordsTrustedCommitSet(t *testing.T) {
	f := newAgentResultTest(t)
	requireWorktreeFeatureCheck(f)
	rm := repo.NewManager(filepath.Join(f.jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	state, extra, err := f.jr.finalizeBuild(context.Background(), rm, false)
	if err != nil || state != "failed" {
		t.Fatalf("state=%s extra=%v err=%v", state, extra, err)
	}
	if !ledgerTrusts(t, f.jr, extra["commit_set"].(map[string]string)) {
		t.Fatal("build with failed checks did not record its resolved commit set")
	}
}

func publishPreviewAndResolve(t *testing.T, f agentResultTest, key string) map[string]string {
	t.Helper()
	if _, err := call(t, f.jr, "platform_publish_preview", previewArguments(t, map[string]any{"idempotency_key": key})); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.jr.previewCommitSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestMidBuildPreviewPublicationRecordsTrustedCommitSet(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "first\n")
	first := publishPreviewAndResolve(t, f, "preview-first")
	writeAgentFile(t, f.checkout, "feature.txt", "second\n")
	second := publishPreviewAndResolve(t, f, "preview-second")
	if first["repo-1"] == second["repo-1"] {
		t.Fatalf("resolutions did not differ: %v", first)
	}
	if !ledgerTrusts(t, f.jr, first) || !ledgerTrusts(t, f.jr, second) {
		t.Fatal("mid-build preview resolutions were not both recorded")
	}
}

func TestAgentCommitClaimsNeverReachTheTrustedLedger(t *testing.T) {
	f := newAgentResultTest(t)
	agentSHA := agentCommit(t, f.checkout, "feature.txt", "agent change\n")
	claimed := strings.Repeat("e", 40)
	args := `{"url":"https://preview.example.test","health":"healthy","expires_at":"2099-01-01T00:00:00Z","commit_set":{"repo-1":"` + claimed + `"}}`
	if _, err := call(t, f.jr, "platform_publish_preview", args); err == nil {
		t.Fatal("preview accepted an agent commit_set claim")
	}
	resolved := publishPreviewAndResolve(t, f, "preview-runner")
	if resolved["repo-1"] == agentSHA {
		t.Fatalf("runner adopted the agent commit %s", agentSHA)
	}
	if !ledgerTrusts(t, f.jr, resolved) {
		t.Fatal("runner-resolved set was not recorded")
	}
	for _, claim := range []string{agentSHA, claimed} {
		if ledgerTrusts(t, f.jr, map[string]string{"repo-1": claim}) {
			t.Fatalf("agent claim %s reached the trusted ledger", claim)
		}
	}
}
