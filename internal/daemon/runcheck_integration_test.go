package daemon_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

const fakeRunCheckAgent = `#!/bin/sh
echo 'package main

func Feature() bool { return true }
' > feature.go
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_run_check","arguments":{"check_id":"feature-file","idempotency_key":"rc-e2e"}}}' \
 | "$RUNNER_BIN" mcp-serve --socket "$PLATFORM_MCP_SOCKET" --token-file "$PLATFORM_MCP_TOKEN_FILE" > mcp-response.jsonl 2>&1
echo '{"type":"result","session_id":"sess-rc","status":"success"}'
`

func TestBuildJobRunCheckEndToEnd(t *testing.T) {
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
	fakeAgent := writeFakeAgent(t, strings.ReplaceAll(fakeRunCheckAgent, "$RUNNER_BIN", runnerBin))

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": fakeAgent}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-rc": {URL: origin}}
	cfg.Checks = map[string]config.CheckConfig{
		"feature-file": {Command: "test", Args: []string{"-f", "feature.go"}},
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
		JobID: "build-rc", AttemptID: "att-1", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-rc", origin)}, ApprovedPlanRevision: 1,
		RequiredChecks: []string{"feature-file"},
		PushBranch:     true, PRTitle: "PROJ-12 add feature",
		Ticket: v1.TicketContext{Key: "PROJ-12", Summary: "Add Feature()"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "job finished", 60*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})

	payload := cp.EventsByKind("job_finished")[0].Payload.(map[string]any)
	if payload["state"] != "succeeded" {
		t.Fatalf("state = %v payload = %v", payload["state"], payload)
	}

	baseCmd := exec.Command("git", "--git-dir", origin, "rev-parse", "main")
	baseOut, err := baseCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("base rev-parse: %v\n%s", err, baseOut)
	}
	base := strings.TrimSpace(string(baseOut))

	events := cp.EventsByKind("test_results")
	if len(events) != 2 {
		t.Fatalf("test_results events = %d", len(events))
	}
	var iterative, terminal map[string]any
	for _, e := range events {
		p := e.Payload.(map[string]any)
		switch p["phase"] {
		case "iterative":
			iterative = p
		case "terminal":
			terminal = p
		}
	}
	if iterative == nil || terminal == nil {
		t.Fatalf("expected one iterative and one terminal event: %v", events)
	}
	if iterative["source"] != "runner" || iterative["result"] != "passed" {
		t.Fatalf("iterative event = %v", iterative)
	}
	if iterative["repository_id"] != "repo-rc" || iterative["commit_sha"] == base || iterative["worktree_dirty"] != false {
		t.Fatalf("iterative provenance = %v", iterative)
	}
	if terminal["result"] != "passed" || terminal["commit_sha"] == base {
		t.Fatalf("terminal event did not execute independently after finalization: %v", terminal)
	}

	responseCmd := exec.Command("git", "--git-dir", origin, "show", "job/build-rc/att-1:mcp-response.jsonl")
	responseOut, err := responseCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run_check response was not captured: %v\n%s", err, responseOut)
	}
	if !strings.Contains(string(responseOut), "passed") || strings.Contains(string(responseOut), `"error"`) {
		t.Fatalf("run_check response = %s", responseOut)
	}
}
