package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/isolation"
)

const configuredDockerClaudeLogin = `{"claudeAiOauth":{"accessToken":"configured","expiresAt":1}}`

func installConfiguredDockerClaudeLogin(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "claude-personal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(configuredDockerClaudeLogin), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func jobHomeCredentials(t *testing.T, request isolation.OwnershipRequest) string {
	t.Helper()
	if len(request.Private) == 0 {
		t.Fatalf("hand-off request has no private roots: %+v", request)
	}
	data, err := os.ReadFile(filepath.Join(request.Private[0], ".claude", ".credentials.json"))
	if err != nil {
		t.Fatalf("read job home credentials: %v", err)
	}
	return string(data)
}

func TestDockerProviderJobCopiesTheConfiguredClaudeConfigDirectoryLogin(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	configDir := installConfiguredDockerClaudeLogin(t)
	run.jr.d.cfg.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": configDir}}
	copied := ""
	run.ownership.onHandOff = func(request isolation.OwnershipRequest) { copied = jobHomeCredentials(t, request) }
	run.execute(t)
	run.expectCalls(t, "handoff:job start remove:a1 reclaim:job")
	if copied != configuredDockerClaudeLogin {
		t.Fatalf("job home credentials = %q, want the CLAUDE_CONFIG_DIR login", copied)
	}
}

func TestDockerProviderJobWithoutClaudeConfigDirCopiesTheDefaultLogin(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	copied := ""
	run.ownership.onHandOff = func(request isolation.OwnershipRequest) { copied = jobHomeCredentials(t, request) }
	run.execute(t)
	if copied != `{"claudeAiOauth":{"accessToken":"test","expiresAt":1}}` {
		t.Fatalf("job home credentials = %q, want the default host login", copied)
	}
}

func TestDockerProviderJobFailsWhenTheConfiguredClaudeLoginIsMissing(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	run.jr.d.cfg.ProviderEnvironment = map[string]map[string]string{
		"claude": {"CLAUDE_CONFIG_DIR": filepath.Join(t.TempDir(), "absent")},
	}
	run.execute(t)
	run.expectCalls(t, "")
	run.expectFinished(t, "failed", string(v1.JobFailureWorkspacePreparation))
}

func newClaudeWorkIntakeOwnershipHarness(t *testing.T, configDir string) *workIntakeOwnershipHarness {
	t.Helper()
	h := newWorkIntakeOwnershipHarness(t)
	h.generator.cfg.WorkIntakeProposal.Adapter = "claude"
	h.generator.cfg.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": configDir}}
	h.generator.runVersion = func(context.Context, string) (string, error) { return "2.1.0 (Claude Code)", nil }
	digest, err := h.generator.ModelExecutionConfigDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.operation.Adapter = "claude"
	h.operation.ModelExecutionConfigDigest = digest
	return h
}

func TestWorkIntakeProposalCopiesTheConfiguredClaudeConfigDirectoryLogin(t *testing.T) {
	h := newClaudeWorkIntakeOwnershipHarness(t, installConfiguredDockerClaudeLogin(t))
	copied := ""
	h.ownership.onHandOff = func(request isolation.OwnershipRequest) { copied = jobHomeCredentials(t, request) }
	h.generator.Generate(context.Background(), h.operation)
	if len(h.ownership.handOffs) != 1 {
		t.Fatalf("hand-offs = %d (calls %s)", len(h.ownership.handOffs), h.log)
	}
	if copied != configuredDockerClaudeLogin {
		t.Fatalf("proposal job home credentials = %q, want the CLAUDE_CONFIG_DIR login", copied)
	}
}

func TestWorkIntakeProposalRefreshesAnExpiringClaudeLoginBeforeCopyingIt(t *testing.T) {
	f, refresher := newRefreshFixture(t, -time.Hour)
	h := newClaudeWorkIntakeOwnershipHarness(t, f.hostDir)
	h.generator.claudeLogins = f.sync
	copied := ""
	h.ownership.onHandOff = func(request isolation.OwnershipRequest) { copied = jobHomeCredentials(t, request) }
	h.generator.Generate(context.Background(), h.operation)
	if refresher.calls != 1 {
		t.Fatalf("refreshes = %d before the consultation, want 1", refresher.calls)
	}
	f.assertHost("r1")
	if !strings.Contains(copied, `"refreshToken":"fake-r1"`) {
		t.Fatal("the consultation job home did not get the refreshed login")
	}
	f.assertNoTokenLogged()
}

func TestNewWorkIntakeProposalGeneratorFailsClosedWithoutAnEgressPolicyVerifier(t *testing.T) {
	cfg := config.Default()
	cfg.EgressPolicyVerifierSocket = filepath.Join(t.TempDir(), "absent.sock")
	attestation := isolation.DockerNetworkAttestation{ID: "network-1", Name: "provider-egress", PolicyID: "consultation-v1"}
	generator := newProviderWorkIntakeProposalGenerator(
		cfg, "runner-1", nil, attestation, egresspolicy.Binding{NetworkID: "network-1"}, testRootlessMapping, nil,
	)
	if generator.cfg.EgressPolicyVerifierSocket != cfg.EgressPolicyVerifierSocket || generator.runnerID != "runner-1" ||
		generator.networkAttestation != attestation || generator.userMapping != testRootlessMapping ||
		generator.probeDocker == nil || generator.attestNetwork == nil || generator.removeContainer == nil {
		t.Fatalf("generator = %+v", generator)
	}
	if err := generator.attestEffectivePolicy(context.Background(), generator.effectivePolicy); err == nil {
		t.Fatal("effective policy attested without a verifier")
	}
	if lease, err := generator.acquireEffectivePolicy(context.Background(), generator.effectivePolicy, time.Second); err == nil {
		_ = lease.Close()
		t.Fatal("effective policy lease acquired without a verifier")
	}
}

func TestWorkIntakeProposalFailsBeforeStartWhenTheConfiguredClaudeLoginIsMissing(t *testing.T) {
	h := newClaudeWorkIntakeOwnershipHarness(t, filepath.Join(t.TempDir(), "absent"))
	result := h.generator.Generate(context.Background(), h.operation)
	if result.Outcome.Kind == v1.WorkIntakeProposalSucceeded {
		t.Fatalf("result = %+v", result)
	}
	if got := h.log.String(); got != "" {
		t.Fatalf("calls = %q, want no hand-off or start", got)
	}
	if want := workIntakeProposalPreStartFailure(); result.Outcome != want.Outcome {
		t.Fatalf("outcome = %+v, want the pre-start failure %+v", result.Outcome, want.Outcome)
	}
}
