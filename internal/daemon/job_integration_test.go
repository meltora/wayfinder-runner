package daemon_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
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
	// Capability discovery invokes the same executable as jobs. A version probe
	// must not execute the fixture's job effects (MCP calls, markers or sleeps).
	versionProbe := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then\n  echo \"controlled-planning-agent 1.0\"\n  exit 0\nfi\n"
	if err := os.WriteFile(path, []byte(versionProbe+script), 0o755); err != nil {
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

func pinnedPlanRepository(t *testing.T, repositoryID, origin string) v1.JobRepository {
	t.Helper()
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = origin
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve observed repository head: %v\n%s", err, output)
	}
	return v1.JobRepository{
		RepositoryID: repositoryID, RepositoryHeadObservationID: "head-observation-" + repositoryID,
		BaseBranch: "main", BaseSHA: strings.TrimSpace(string(output)), Primary: true,
	}
}

func makeTestPack(t *testing.T) (archive []byte, pubKeyB64 string, signingKey ed25519.PrivateKey) {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "WORKFLOW.md"), []byte("# platform workflow"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	options := agentpack.ArchiveBuildOptions{PackKey: "default-pack", Provider: "claude-code", Version: "1.0.0"}
	if _, err := agentpack.BuildArchiveFromDirectory(src, options, &buf); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), base64.StdEncoding.EncodeToString(der), privateKey
}

func signedIntegrationPackAuthority(t *testing.T, reference v1.AgentPackRef, signingKey ed25519.PrivateKey) *v1.SignedAgentPackAuthority {
	t.Helper()
	authority := v1.SignedAgentPackAuthority{
		SchemaVersion: 1, RegistryVersionID: "pack-version-1",
		Scope:   v1.AgentPackAuthorityScope{Kind: v1.AgentPackScopePlatform},
		PackKey: "default-pack", PackName: "Default Pack", Provider: "claude-code",
		ProtocolVersion: 1, MinimumRunnerVersion: "0.1.0", AgentPack: reference,
	}
	digest, err := agentpack.PublicationAttestationDigest(authority)
	if err != nil {
		t.Fatal(err)
	}
	authority.AttestationDigest = digest
	digestBytes, _ := hex.DecodeString(digest)
	authority.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(signingKey, digestBytes))
	return &authority
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
	runnerBin := buildRunnerBinary(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
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
	pinned := pinnedPlanRepository(t, "repo-1", origin)
	planningGit(t, origin, "commit", "--allow-empty", "-m", "advance after capture")
	advancedSHA := planningGit(t, origin, "rev-parse", "HEAD")
	if advancedSHA == pinned.BaseSHA {
		t.Fatal("fixture branch must advance after capture")
	}
	packBytes, pub, packSigningKey := makeTestPack(t)
	archiveSum := sha256.Sum256(packBytes)
	digest := hex.EncodeToString(archiveSum[:])
	packPath := cp.ServePack("base-1.0.0.tar.gz", packBytes)

	receipt := filepath.Join(t.TempDir(), "planning-resources")
	checks := `#!/bin/sh
set -eu
[ "$(git rev-parse HEAD)" = "` + pinned.BaseSHA + `" ]
if git symbolic-ref -q HEAD >/dev/null; then exit 1; fi
printf '%s\n' "$PWD" "$PLATFORM_MCP_SOCKET" "$PLATFORM_MCP_TOKEN_FILE" > '` + strings.ReplaceAll(receipt, "'", "'\"'\"'") + `'
`
	fakeAgent := writeFakeAgent(t, checks+strings.ReplaceAll(fakeAgentScript, "$RUNNER_BIN", runnerBin))

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{
		"claude":   fakeAgent,
		"codex":    filepath.Join(home, "unavailable-codex"),
		"opencode": filepath.Join(home, "unavailable-opencode"),
	}
	cfg.AgentPackPublicKeys = map[string]string{"test-pack": pub}
	cfg.AllowedArtifactOrigins = []string{cp.SecureHTTPURL()}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop after planning test")
		}
	}()

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	packReference := &v1.AgentPackRef{
		Version: "1.0.0", Digest: digest,
		URL: cp.SecureHTTPURL() + packPath, KeyID: "test-pack",
	}
	awaitConfiguredPlanningCapability(t, cp, stateDir, fakeAgent)

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "job-1", AttemptID: "att-1", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories:       []v1.JobRepository{pinned},
		Ticket:             v1.TicketContext{Key: "PROJ-1", Summary: "Add feature X", Description: "Details"},
		AgentPack:          packReference,
		AgentPackAuthority: signedIntegrationPackAuthority(t, *packReference, packSigningKey),
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

	discoveryEvents := cp.EventsByKind(v1.EventKindAgentConfigDiscovery)
	if len(discoveryEvents) != 1 {
		t.Fatalf("discovery events = %d, want one per repository", len(discoveryEvents))
	}
	discovery := discoveryEvents[0].Payload.(map[string]any)
	if discovery["project_id"] != job.ProjectID || discovery["repository_id"] != "repo-1" ||
		discovery["catalog_version"] != v1.AgentConfigCatalogVersion || discovery["commit_sha"] != pinned.BaseSHA {
		t.Fatalf("discovery authority = %v", discovery)
	}
	if len(cp.EventsByKind("progress")) == 0 {
		t.Fatal("expected progress events")
	}
	assertPlanningResourcesRemoved(t, receipt)
	if got := planningGit(t, origin, "rev-parse", "HEAD"); got != advancedSHA {
		t.Fatalf("planning changed origin HEAD: %s", got)
	}
	if got := planningGit(t, origin, "status", "--porcelain"); got != "" {
		t.Fatalf("planning changed origin worktree: %s", got)
	}
}

func TestPlanningJobFailsClosedBeforeProviderWhenRepositoryConfigurationNeedsApproval(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(origin, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "add", ".mcp.json")
	commit.Dir = origin
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("stage review-required configuration: %v\n%s", err, output)
	}
	commit = exec.Command("git", "commit", "-m", "add mcp configuration")
	commit.Dir = origin
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit review-required configuration: %v\n%s", err, output)
	}

	providerMarker := filepath.Join(t.TempDir(), "provider-started")
	fakeAgent := writeFakeAgent(t, "#!/bin/sh\ntouch \""+providerMarker+"\"\n")
	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": fakeAgent}
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
		JobID: "job-config-review", AttemptID: "att-1", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-1", origin)},
		Ticket:       v1.TicketContext{Key: "PROJ-12", Summary: "Review repository configuration"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job rejected before provider startup", 30*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) >= 1
	})

	terminal := cp.EventsByKind("job_finished")[0].Payload.(map[string]any)
	if terminal["state"] != "failed" || terminal["failure_code"] != "workspace_preparation_failed" ||
		terminal["failure_message"] != "workspace preparation failed" {
		t.Fatalf("terminal event = %v", terminal)
	}
	if _, err := os.Stat(providerMarker); !os.IsNotExist(err) {
		t.Fatalf("provider was started before configuration approval: %v", err)
	}
	discoveryEvents := cp.EventsByKind(v1.EventKindAgentConfigDiscovery)
	if len(discoveryEvents) != 1 {
		t.Fatalf("discovery events = %d, want one durable event", len(discoveryEvents))
	}
	discovery := discoveryEvents[0].Payload.(map[string]any)
	items := discovery["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["path"] != ".mcp.json" || items[0].(map[string]any)["decision"] != "pending" {
		t.Fatalf("discovery items = %v", items)
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
		Repositories: []v1.JobRepository{pinnedPlanRepository(t, "repo-2", origin)},
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

func TestPlanningJobRenewsBeforeItsOriginalLeaseExpires(t *testing.T) {
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
	agent := writeFakeAgent(t, "#!/bin/sh\nsleep 8\necho '{\"type\":\"result\",\"status\":\"success\"}'\n")
	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": agent}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-renew": {URL: origin}}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	job := withIntegrationAuthority(v1.JobSpec{
		JobID: "job-renew", AttemptID: "att-renew", Kind: v1.JobKindPlan, FencingToken: 9,
		LeaseExpiresAt: time.Now().Add(6 * time.Second),
		Repositories:   []v1.JobRepository{pinnedPlanRepository(t, "repo-renew", origin)},
		Ticket:         v1.TicketContext{Key: "PROJ-RENEW", Summary: "Long planning job"},
	})
	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "lease renewal request", 8*time.Second, func() bool {
		return len(cp.EventsByKind("lease_renewal")) == 1
	})
	renewal := cp.EventsByKind("lease_renewal")[0].Payload.(map[string]any)
	if renewal["lease_id"] != job.LeaseID || renewal["manifest_id"] != job.ManifestID ||
		renewal["manifest_digest"] != job.ManifestDigest || renewal["fencing_token"] != float64(job.FencingToken) {
		t.Fatalf("renewal authority = %v", renewal)
	}
	if err := cp.SendCommand(ctx, v1.CommandPayload{
		Command: v1.CmdRenewLease, AttemptID: job.AttemptID, FencingToken: job.FencingToken,
		LeaseExpiresAt: time.Now().Add(8 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job completion after original expiry", 12*time.Second, func() bool {
		return len(cp.EventsByKind("job_finished")) == 1
	})
	terminal := cp.EventsByKind("job_finished")[0].Payload.(map[string]any)
	if terminal["state"] != "succeeded" {
		t.Fatalf("terminal after renewal = %v", terminal)
	}
}

var _ = json.Marshal
