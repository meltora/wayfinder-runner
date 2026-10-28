package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func TestProviderEnvironmentReachesOnlyTheAgentProcess(t *testing.T) {
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
	dumps := t.TempDir()
	agentDump := filepath.Join(dumps, "agent")
	checkDump := filepath.Join(dumps, "check")
	accountDir := filepath.Join(t.TempDir(), "claude-xogito")

	runnerBin := buildRunnerBinary(t)
	script := "printf '%s' \"${CLAUDE_CONFIG_DIR-unset}\" > '" + agentDump + "'\n" +
		strings.TrimPrefix(strings.ReplaceAll(fakeBuildAgent, "$RUNNER_BIN", runnerBin), "#!/bin/sh\n")
	fakeAgent := writeFakeAgent(t, script)

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": fakeAgent}
	cfg.ProviderEnvironment = map[string]map[string]string{
		"claude": {"CLAUDE_CONFIG_DIR": accountDir},
		"codex":  {"CODEX_HOME": "/must-not-apply"},
	}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	cfg.Checks = map[string]config.CheckConfig{
		"environment": {Command: "/bin/sh", Args: []string{"-c", "printf '%s|%s' \"${CLAUDE_CONFIG_DIR-unset}\" \"${CODEX_HOME-unset}\" > '" + checkDump + "'"}},
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
		JobID: "build-env", AttemptID: "att-env", Kind: v1.JobKindBuild, FencingToken: 1,
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-1", origin)}, ApprovedPlanRevision: 1, ApprovedPlan: daemon.ApprovedPlanFixture(1),
		RequiredChecks: []string{"environment"},
		Ticket:         v1.TicketContext{Key: "PROJ-10", Summary: "Environment"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job finished", 60*time.Second, func() bool { return len(cp.EventsByKind("job_finished")) >= 1 })

	agent, err := os.ReadFile(agentDump)
	if err != nil {
		t.Fatal(err)
	}
	if string(agent) != accountDir {
		t.Fatalf("agent CLAUDE_CONFIG_DIR = %q", agent)
	}
	check, err := os.ReadFile(checkDump)
	if err != nil {
		t.Fatal(err)
	}
	if string(check) != "unset|unset" {
		t.Fatalf("check process environment = %q", check)
	}
	for _, envelope := range cp.Events() {
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), accountDir) || strings.Contains(string(raw), "CLAUDE_CONFIG_DIR") {
			t.Fatal("provider environment value reached control-plane events")
		}
	}
}
