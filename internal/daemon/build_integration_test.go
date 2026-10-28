package daemon_test

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func seedBareOrigin(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "seed")
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	git(work, "init", "-b", "main")
	git(work, "config", "user.email", "t@t")
	git(work, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "commit", "-m", "init")
	bare := filepath.Join(base, "origin.git")
	git(base, "clone", "--bare", work, bare)
	return bare
}

const fakeBuildAgent = `#!/bin/sh
echo '{"type":"status","status":"starting"}'
echo 'package main

func Feature() bool { return true }
' > feature.go
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{}}}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"platform_publish_test_results","arguments":{"suite":"unit","command":"go build ./...","result":"passed","duration_ms":42,"idempotency_key":"tr-1"}}}' \
 '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"platform_checkpoint","arguments":{"plan_step":"implemented","next_action":"review","idempotency_key":"cp-1"}}}' \
 '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"platform_mark_review_ready","arguments":{"summary":"feature implemented","idempotency_key":"rr-1"}}}' \
 | "$RUNNER_BIN" mcp-serve --socket "$PLATFORM_MCP_SOCKET" --token-file "$PLATFORM_MCP_TOKEN_FILE" >/dev/null 2>&1
echo '{"type":"result","session_id":"sess-123","status":"success"}'
`

const fakeFailingBuildAgent = `#!/bin/sh
echo 'package main

func PartialFeature() bool { return true }
' > partial.go
if [ -n "$RUNNER_BIN" ]; then
  printf '%s\n' \
   '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_checkpoint","arguments":{"plan_step":"partial implementation","branch_state":"agent-controlled-not-authoritative","next_action":"resume","idempotency_key":"capacity-cp-1"}}}' \
   | "$RUNNER_BIN" mcp-serve --socket "$PLATFORM_MCP_SOCKET" --token-file "$PLATFORM_MCP_TOKEN_FILE" >/dev/null 2>&1
fi
echo '{"type":"error","status":"failed","session_id":"capacity-session-1","error":{"name":"APIError","data":{"message":"Payment Required: an active membership could not be verified","statusCode":402,"isRetryable":false}}}'
exit 17
`

func TestBuildJobEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cp.Close()

	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	origin := seedBareOrigin(t)

	runnerBin := buildRunnerBinary(t)
	fakeAgent := writeFakeAgent(t, strings.ReplaceAll(fakeBuildAgent, "$RUNNER_BIN", runnerBin))

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": fakeAgent}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	cfg.Checks = map[string]config.CheckConfig{
		"feature-file":   {Command: "test", Args: []string{"-f", "feature.go"}},
		"feature-symbol": {Command: "grep", Args: []string{"-q", "Feature", "feature.go"}},
	}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "build-1", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-1", origin)}, ApprovedPlanRevision: 1, ApprovedPlan: daemon.ApprovedPlanFixture(1),
		RequiredChecks: []string{"feature-file", "feature-symbol"},
		PushBranch:     true, PRTitle: "PROJ-9 add feature",
		Ticket: v1.TicketContext{Key: "PROJ-9", Summary: "Add Feature()"},
	})
	job.AgentRunAuthority = &v1.AgentRunAuthority{
		SchemaVersion: 1, AgentRunID: "agent-run-build-1",
		StepRunID: "step-run-build-1", StepSequence: 2,
	}
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "job finished", 60*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})

	finished := cp.EventsByKind("job_finished")
	payload := finished[0].Payload.(map[string]any)
	if payload["state"] != "succeeded" {
		t.Fatalf("state = %v payload = %v", payload["state"], payload)
	}
	if payload["manifest_kind"] != "build" || payload["lease_id"] != job.LeaseID ||
		payload["manifest_id"] != job.ManifestID || payload["manifest_digest"] != job.ManifestDigest {
		t.Fatalf("terminal authority = %v", payload)
	}

	startedEvents := cp.EventsByKind("job_started")
	if len(startedEvents) != 1 {
		t.Fatalf("job_started events = %d, want 1", len(startedEvents))
	}
	started := startedEvents[0].Payload.(map[string]any)
	if started["manifest_kind"] != "build" || started["lease_id"] != job.LeaseID ||
		started["attempt_id"] != job.AttemptID || started["manifest_id"] != job.ManifestID ||
		started["manifest_digest"] != job.ManifestDigest {
		t.Fatalf("job_started authority = %v", started)
	}
	agentAuthority, ok := started["agent_run_authority"].(map[string]any)
	if !ok || agentAuthority["agent_run_id"] != job.AgentRunAuthority.AgentRunID ||
		agentAuthority["step_run_id"] != job.AgentRunAuthority.StepRunID ||
		agentAuthority["step_sequence"] != float64(job.AgentRunAuthority.StepSequence) {
		t.Fatalf("job_started AgentRun authority = %v", started["agent_run_authority"])
	}
	assertJobStartedBeforeEvidence(t, cp.Events())

	if len(cp.EventsByKind("test_results")) != 3 {
		t.Fatalf("test_results events = %d", len(cp.EventsByKind("test_results")))
	}
	reviewEvents := cp.EventsByKind("review_ready")
	if len(reviewEvents) != 1 {
		t.Fatal("missing review_ready")
	}
	review := reviewEvents[0].Payload.(map[string]any)
	if review["summary"] != "feature implemented" {
		t.Fatalf("review summary = %v", review["summary"])
	}
	if _, leaked := review["idempotency_key"]; leaked {
		t.Fatalf("review event leaked local idempotency key: %v", review)
	}
	if len(cp.EventsByKind("pr_ready")) != 0 {
		t.Fatal("runner emitted obsolete pr_ready event")
	}

	cmd := exec.Command("git", "--git-dir", origin, "rev-parse", "job/build-1/att-1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("branch not pushed: %v\n%s", err, out)
	}
	commitSet := review["commit_set"].(map[string]any)
	if commitSet["repo-1"] != strings.TrimSpace(string(out)) {
		t.Fatalf("review commit set = %v, pushed head = %s", commitSet, out)
	}
}

func assertJobStartedBeforeEvidence(t *testing.T, events []v1.Envelope) {
	t.Helper()
	startedAt := -1
	sawSanitizedAgentStatus := false
	for index, envelope := range events {
		payload, ok := envelope.Payload.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := payload["kind"].(string)
		if kind == "job_started" {
			startedAt = index
			continue
		}
		if kind == "progress" && payload["milestone"] == "agent_started" {
			t.Fatal("runner emitted the misleading pre-start agent_started milestone")
		}
		if kind == "sanitized_log" {
			sawSanitizedAgentStatus = true
		}
		if kind == "sanitized_log" || kind == "test_results" || kind == "checkpoint" || kind == "review_ready" || kind == "job_finished" {
			if startedAt < 0 || startedAt >= index {
				t.Fatalf("%s arrived before durable job_started evidence", kind)
			}
		}
	}
	if startedAt < 0 {
		t.Fatal("job_started was not observed")
	}
	if !sawSanitizedAgentStatus {
		t.Fatal("immediate provider output was not consumed after job_started")
	}
}

func TestProviderStartFailureEmitsNoJobStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cp.Close()

	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	origin := seedBareOrigin(t)
	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": filepath.Join(t.TempDir(), "missing-agent")}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "build-start-failure", AttemptID: "attempt-start-failure",
		Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories:         []v1.JobRepository{pinnedPlanRepository(t, "repo-1", origin)},
		ApprovedPlanRevision: 1, ApprovedPlan: daemon.ApprovedPlanFixture(1),
		Ticket: v1.TicketContext{Key: "PROJ-START", Summary: "Fail before process start"},
	})
	job.AgentRunAuthority = &v1.AgentRunAuthority{
		SchemaVersion: 1, AgentRunID: "agent-run-start-failure",
		StepRunID: "step-run-start-failure", StepSequence: 1,
	}
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job start failure", 30*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) == 1
	})
	if events := cp.EventsByKind("job_started"); len(events) != 0 {
		t.Fatalf("start failure emitted job_started: %v", events)
	}
	finished := cp.EventsByKind("job_finished")[0].Payload.(map[string]any)
	if finished["state"] != "failed" || finished["failure_code"] != "agent_start_failed" {
		t.Fatalf("terminal start failure = %v", finished)
	}
}

func TestBuildJobCheckFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cp.Close()

	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	origin := seedBareOrigin(t)
	runnerBin := buildRunnerBinary(t)
	fakeAgent := writeFakeAgent(t, strings.ReplaceAll(fakeBuildAgent, "$RUNNER_BIN", runnerBin))

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.ControlPlaneHTTPURL = cp.EnrollURL()
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": fakeAgent}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-2": {URL: origin}}
	cfg.Checks = map[string]config.CheckConfig{"always-fail": {Command: "false"}}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "build-2", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-2", origin)}, ApprovedPlanRevision: 1, ApprovedPlan: daemon.ApprovedPlanFixture(1),
		RequiredChecks: []string{"always-fail"},
		PushBranch:     true,
		Ticket:         v1.TicketContext{Key: "PROJ-10", Summary: "Will fail checks"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "job finished", 60*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})
	payload := cp.EventsByKind("job_finished")[0].Payload.(map[string]any)
	if payload["state"] != "failed" {
		t.Fatalf("state = %v", payload["state"])
	}
	if payload["failure_code"] != "required_checks_failed" {
		t.Fatalf("failure evidence = %v", payload)
	}
	failed := 0
	for _, e := range cp.EventsByKind("test_results") {
		if e.Payload.(map[string]any)["result"] == "failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed check events = %d", failed)
	}
	if len(cp.EventsByKind("artifact_registered")) != 0 {
		t.Fatalf("receipt-backed failed attempt emitted generic artifact metadata = %d", len(cp.EventsByKind("artifact_registered")))
	}
	assertStoredBuildPatch(t, cp, origin, job)
	if len(cp.EventsByKind("pr_ready")) != 0 {
		t.Fatal("failed checks emitted pr_ready")
	}
	if len(cp.EventsByKind("job_started")) != 0 {
		t.Fatal("legacy unbound build emitted protected job_started evidence")
	}
	if len(cp.EventsByKind("checkpoint")) != 1 || len(cp.EventsByKind("test_results")) == 0 {
		t.Fatal("legacy unbound build lost its established tool/evidence flow")
	}
	cmd := exec.Command("git", "--git-dir", origin, "show", "job/build-2/att-1:feature.go")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "Feature") {
		t.Fatalf("failed attempt branch is not recoverable: %v\n%s", err, out)
	}
}

func TestBuildJobAgentFailurePreservesPartialWork(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cp.Close()

	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	origin := seedBareOrigin(t)
	runnerBin := buildRunnerBinary(t)
	fakeAgent := writeFakeAgent(t, strings.ReplaceAll(fakeFailingBuildAgent, "$RUNNER_BIN", runnerBin))

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.ControlPlaneHTTPURL = cp.EnrollURL()
	cfg.Provider = "opencode"
	cfg.ProviderBinaries = map[string]string{"opencode": fakeAgent}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-agent-failure": {URL: origin}}
	cfg.Checks = map[string]config.CheckConfig{
		"partial-file": {Command: "test", Args: []string{"-f", "partial.go"}},
		"always-fail":  {Command: "false"},
	}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "build-agent-failure", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-agent-failure", origin)}, ApprovedPlanRevision: 1, ApprovedPlan: daemon.ApprovedPlanFixture(1),
		RequiredChecks: []string{"partial-file", "always-fail"}, PushBranch: true,
		ProviderCapacityResumePolicy: v1.CapacityResumeAutomatic,
		Ticket:                       v1.TicketContext{Key: "PROJ-11", Summary: "Preserve partial work"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "provider capacity recorded", 60*time.Second, func() bool {
		return len(cp.EventsByKind("provider_capacity_exhausted")) >= 1
	})
	if got := len(cp.EventsByKind("job_finished")); got != 0 {
		t.Fatalf("provider capacity emitted %d competing terminal outcomes", got)
	}
	if len(cp.EventsByKind("artifact_registered")) != 0 {
		t.Fatalf("receipt-backed agent failure emitted generic artifact metadata = %d", len(cp.EventsByKind("artifact_registered")))
	}
	assertStoredBuildPatch(t, cp, origin, job)
	if len(cp.EventsByKind("pr_ready")) != 0 {
		t.Fatal("agent failure emitted pr_ready")
	}
	capacityEvents := cp.EventsByKind("provider_capacity_exhausted")
	if len(capacityEvents) != 1 {
		t.Fatalf("provider capacity events = %d", len(capacityEvents))
	}
	capacity := capacityEvents[0].Payload.(map[string]any)
	if capacity["adapter"] != "opencode" || capacity["provider"] != "opencode" ||
		capacity["reason"] != "quota_exhausted" || capacity["resume_policy"] != "automatic" {
		t.Fatalf("provider capacity payload = %v", capacity)
	}
	if capacity["manifest_kind"] != "build" || capacity["lease_id"] != job.LeaseID ||
		capacity["job_id"] != job.JobID || capacity["attempt_id"] != job.AttemptID ||
		capacity["manifest_id"] != job.ManifestID || capacity["manifest_digest"] != job.ManifestDigest ||
		capacity["fencing_token"] != float64(job.FencingToken) || capacity["observed_at"] == "" {
		t.Fatalf("provider capacity authority = %v", capacity)
	}
	if capacity["branch_ref"] != "job/build-agent-failure/att-1" || capacity["commit_sha"] == "" ||
		capacity["checkpoint_ref"] != "att-1" {
		t.Fatalf("provider capacity lineage = %v", capacity)
	}
	checkpoint, err := collector.NewStore(stateDir).CheckpointForAttempt(job.JobID, job.AttemptID, job.FencingToken)
	if err != nil {
		t.Fatalf("load emitted recovery checkpoint: %v", err)
	}
	wantBranchState := capacity["branch_ref"].(string) + "@" + capacity["commit_sha"].(string)
	if checkpoint.BranchState != wantBranchState {
		t.Fatalf("capacity checkpoint was overwritten after emission: %#v", checkpoint)
	}
	cmd := exec.Command("git", "--git-dir", origin, "show", "job/build-agent-failure/att-1:partial.go")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "PartialFeature") {
		t.Fatalf("agent failure branch is not recoverable: %v\n%s", err, out)
	}
}

func assertStoredBuildPatch(t *testing.T, cp *mockcp.Server, origin string, job v1.JobSpec) {
	t.Helper()
	branch := "job/" + job.JobID + "/" + job.AttemptID
	head, err := exec.Command("git", "--git-dir", origin, "rev-parse", branch).Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(head))
	patch, err := exec.Command("git", "--git-dir", origin, "diff", "--no-ext-diff", "--no-textconv", job.Repositories[0].BaseSHA+".."+sha).Output()
	if err != nil || len(patch) == 0 {
		t.Fatalf("retained branch patch=%q error=%v", patch, err)
	}
	name := "changes-" + job.Repositories[0].RepositoryID + "-" + sha[:8] + ".patch"
	stored, ok := cp.Artifact(job.JobID, job.AttemptID, name)
	if !ok || string(stored) != string(patch) || sha256.Sum256(stored) != sha256.Sum256(patch) {
		t.Fatalf("stored patch does not match exact retained branch: found=%t", ok)
	}
}

func TestMultiRepositoryBuildProducesExactCommitSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cp.Close()

	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	primaryOrigin := seedBareOrigin(t)
	secondaryOrigin := seedBareOrigin(t)
	agent := writeFakeAgent(t, `#!/bin/sh
echo primary > primary.txt
echo secondary > ../secondary/secondary.txt
echo '{"type":"result","status":"success"}'
`)

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": agent}
	cfg.Repositories = map[string]config.RepositoryConfig{
		"primary":   {URL: primaryOrigin},
		"secondary": {URL: secondaryOrigin},
	}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "multi-1", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		ApprovedPlanRevision: 1, ApprovedPlan: daemon.ApprovedPlanFixture(1),
		Repositories: []v1.JobRepository{
			pinnedPlanRepository(t, "primary", primaryOrigin),
			{RepositoryID: "secondary", BaseBranch: "main", BaseSHA: pinnedPlanRepository(t, "secondary", secondaryOrigin).BaseSHA},
		},
		PushBranch: true,
		Ticket:     v1.TicketContext{Key: "PROJ-11", Summary: "Cross-repository change"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "multi-repository job finished", 60*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})
	payload := cp.EventsByKind("job_finished")[0].Payload.(map[string]any)
	if payload["state"] != "succeeded" {
		t.Fatalf("state = %v payload=%v", payload["state"], payload)
	}
	if payload["manifest_kind"] != "build" || payload["manifest_id"] != job.ManifestID {
		t.Fatalf("terminal authority = %v", payload)
	}
	if len(cp.EventsByKind("pr_ready")) != 0 || len(cp.EventsByKind("review_ready")) != 0 {
		t.Fatal("build without an explicit review intent published review events")
	}
	discoveryEvents := cp.EventsByKind(v1.EventKindAgentConfigDiscovery)
	if len(discoveryEvents) != 2 {
		t.Fatalf("discovery events = %d, want one per repository", len(discoveryEvents))
	}
	discoveredRepositories := map[string]bool{}
	for _, event := range discoveryEvents {
		payload := event.Payload.(map[string]any)
		discoveredRepositories[payload["repository_id"].(string)] = true
	}
	if !discoveredRepositories["primary"] || !discoveredRepositories["secondary"] {
		t.Fatalf("discovered repositories = %v", discoveredRepositories)
	}
	for _, origin := range []string{primaryOrigin, secondaryOrigin} {
		cmd := exec.Command("git", "--git-dir", origin, "rev-parse", "job/multi-1/att-1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("branch not pushed to %s: %v\n%s", origin, err, out)
		}
	}
}
