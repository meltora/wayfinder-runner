package daemon_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func buildRunnerBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "our-runner")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/example/app-runner/cmd/our-runner")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build runner binary: %v\n%s", err, out)
	}
	return bin
}

func writeFakeAgent(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func seedGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# repo instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	return dir
}

func makeTestPack(t *testing.T) (archive []byte, digest string, pubKeyB64 string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "WORKFLOW.md"), []byte("# platform workflow"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	d, err := agentpack.PackDir(src, "1.0.0", "", &buf, key)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := agentpack.PublicKeyToBase64(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), d, pub
}

const fakeAgentScript = `#!/bin/sh
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize"}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{}}}' \
 '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"platform_report_progress","arguments":{"milestone":"analysis","current_task":"reading repo","idempotency_key":"prog-1"}}}' \
 '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"platform_publish_plan","arguments":{"plan":{"behavior":"Add feature X","in_scope":["Feature X"],"out_of_scope":[],"assumptions":[],"unanswered_questions":[],"architecture":"TDD in small steps","api_impact":"No public API change","data_impact":"No data change","security_impact":"No new trust boundary","operational_impact":"No runtime change","preview_requirements":"No preview required","automated_test_strategy":"Unit and integration tests","manual_test_plan":"Verify Feature X","rollout_strategy":"Merge after review","rollback_strategy":"Revert the change","risks":["Implementation regression"],"alternatives":[],"dependencies":[],"delivery_order":["Implement","test"]},"change_summary":"Initial implementation plan","idempotency_key":"plan-1"}}}' \
 | "$RUNNER_BIN" mcp-serve --socket "$PLATFORM_MCP_SOCKET" --token-file "$PLATFORM_MCP_TOKEN_FILE" >/dev/null 2>&1
echo '{"type":"result","status":"success"}'
`

func TestPlanningJobEndToEnd(t *testing.T) {
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

	origin := seedGitRepo(t)
	packBytes, digest, pub := makeTestPack(t)
	packPath := cp.ServePack("base-1.0.0.tar.gz", packBytes)

	runnerBin := buildRunnerBinary(t)
	fakeAgent := writeFakeAgent(t, strings.ReplaceAll(fakeAgentScript, "$RUNNER_BIN", runnerBin))

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": fakeAgent}
	cfg.AgentPackPublicKeys = map[string]string{"test-pack": pub}
	cfg.AllowedArtifactOrigins = []string{cp.EnrollURL()}
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
		JobID: "job-1", AttemptID: "att-1", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:       v1.TicketContext{Key: "PROJ-1", Summary: "Add feature X", Description: "Details"},
		AgentPack: &v1.AgentPackRef{
			Version: "1.0.0", Digest: digest,
			URL: cp.EnrollURL() + packPath, KeyID: "test-pack",
		},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	defer func() {
		if t.Failed() {
			for _, e := range cp.Events() {
				raw, _ := json.Marshal(e.Payload)
				t.Logf("event: %s %s", e.Type, raw)
			}
		}
	}()

	waitFor(t, "plan published", 30*time.Second, func() bool {
		return len(cp.EventsByKind("plan_published")) >= 1
	})
	waitFor(t, "job finished", 30*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})

	planEvents := cp.EventsByKind("plan_published")
	event := planEvents[0].Payload.(map[string]any)
	plan := event["plan"].(map[string]any)
	if plan["behavior"] != "Add feature X" {
		t.Fatalf("plan behavior = %v", plan["behavior"])
	}
	if event["fencing_token"].(float64) != 1 || event["manifest_kind"] != "plan" || event["manifest_id"] != "manifest-job-1" {
		t.Fatalf("plan authority = %v", event)
	}

	finished := cp.EventsByKind("job_finished")
	terminal := finished[0].Payload.(map[string]any)
	state := terminal["state"]
	if state != "succeeded" {
		t.Fatalf("job state = %v", state)
	}
	if terminal["manifest_kind"] != "plan" || terminal["lease_id"] != job.LeaseID ||
		terminal["manifest_id"] != job.ManifestID || terminal["manifest_digest"] != job.ManifestDigest {
		t.Fatalf("terminal authority = %v", terminal)
	}

	if len(cp.EventsByKind("config_inventory")) == 0 {
		t.Fatal("expected config inventory event")
	}
	if len(cp.EventsByKind("progress")) == 0 {
		t.Fatal("expected progress events")
	}
}

func TestEmergencyStopCancelsJob(t *testing.T) {
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

	sleepyAgent := writeFakeAgent(t, "#!/bin/sh\nsleep 60\n")
	origin := seedGitRepo(t)

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": sleepyAgent}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-2": {URL: origin}}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "job-2", AttemptID: "att-2", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-2", Primary: true}},
		Ticket:       v1.TicketContext{Key: "PROJ-2", Summary: "Long job"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdEmergencyStop}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job finished after stop", 15*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})
	terminal := cp.EventsByKind("job_finished")[0].Payload.(map[string]any)
	if terminal["state"] != "cancelled" || terminal["failure_code"] != "cancelled" {
		t.Fatalf("cancel terminal = %v", terminal)
	}
	if !d.Paused() {
		t.Fatal("runner should be paused after emergency stop")
	}
}

var _ = json.Marshal
