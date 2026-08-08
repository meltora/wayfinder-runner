package daemon_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
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

	job := v1.JobSpec{
		JobID: "build-1", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}}, ApprovedPlanRevision: 1,
		RequiredChecks: []string{"feature-file", "feature-symbol"},
		PushBranch:     true, PRTitle: "PROJ-9 add feature",
		Ticket: v1.TicketContext{Key: "PROJ-9", Summary: "Add Feature()"},
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
	if payload["pushed"] != true {
		t.Fatalf("expected push: %v", payload)
	}

	if len(cp.EventsByKind("test_results")) != 3 {
		t.Fatalf("test_results events = %d", len(cp.EventsByKind("test_results")))
	}
	if len(cp.EventsByKind("review_ready")) != 1 {
		t.Fatal("missing review_ready")
	}
	if len(cp.EventsByKind("pr_ready")) != 1 {
		t.Fatal("missing pr_ready")
	}

	cmd := exec.Command("git", "--git-dir", origin, "rev-parse", "job/build-1/att-1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("branch not pushed: %v\n%s", err, out)
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

	job := v1.JobSpec{
		JobID: "build-2", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-2", Primary: true}}, ApprovedPlanRevision: 1,
		RequiredChecks: []string{"always-fail"},
		Ticket:         v1.TicketContext{Key: "PROJ-10", Summary: "Will fail checks"},
	}
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
	failed := 0
	for _, e := range cp.EventsByKind("test_results") {
		if e.Payload.(map[string]any)["result"] == "failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed check events = %d", failed)
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

	job := v1.JobSpec{
		JobID: "multi-1", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		ApprovedPlanRevision: 1,
		Repositories: []v1.JobRepository{
			{RepositoryID: "primary", Primary: true},
			{RepositoryID: "secondary"},
		},
		PushBranch: true,
		Ticket:     v1.TicketContext{Key: "PROJ-11", Summary: "Cross-repository change"},
	}
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
	commitSet, ok := payload["commit_set"].(map[string]any)
	if !ok || len(commitSet) != 2 {
		t.Fatalf("commit set = %#v", payload["commit_set"])
	}
	if len(cp.EventsByKind("pr_ready")) != 2 {
		t.Fatalf("pr_ready events = %d", len(cp.EventsByKind("pr_ready")))
	}
	for _, origin := range []string{primaryOrigin, secondaryOrigin} {
		cmd := exec.Command("git", "--git-dir", origin, "rev-parse", "job/multi-1/att-1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("branch not pushed to %s: %v\n%s", origin, err, out)
		}
	}
}
