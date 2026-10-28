package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/provider"
	"github.com/example/app-runner/internal/runlock"
)

type providerCLIProbeCall struct {
	image, binary, runnerID string
	mapping                 isolation.UserMapping
}

func readProviderHelpFixture(t *testing.T, version string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "provider", "testdata", "claude-help-"+version+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func withProviderCLIProbe(t *testing.T, result isolation.ProviderCLIProbe, err error) *[]providerCLIProbeCall {
	t.Helper()
	var calls []providerCLIProbeCall
	original := probeProviderCLI
	probeProviderCLI = func(_ context.Context, image, binary, runnerID string, mapping isolation.UserMapping) (isolation.ProviderCLIProbe, error) {
		calls = append(calls, providerCLIProbeCall{image: image, binary: binary, runnerID: runnerID, mapping: mapping})
		return result, err
	}
	t.Cleanup(func() { probeProviderCLI = original })
	return &calls
}

func compatibleClaudeProbe(t *testing.T) isolation.ProviderCLIProbe {
	return isolation.ProviderCLIProbe{Version: "2.1.299 (Claude Code)", Help: readProviderHelpFixture(t, "2.1.299")}
}

func claudeDockerConfig(t *testing.T) config.Config {
	t.Helper()
	installMappingDocker(t, rootlessMapOutput())
	cfg := enrolledRestrictedDockerConfig(t)
	cfg.Provider = "claude"
	return cfg
}

func assertStartRefusalReleasedTheLock(t *testing.T, cfg config.Config) {
	t.Helper()
	lock, err := runlock.Acquire(cfg.StateDir)
	if err != nil {
		t.Fatalf("refused start kept the run lock: %v", err)
	}
	_ = lock.Close()
}

func TestNewRefusesAJobImageWhoseClaudeRejectsTheRunnerOptions(t *testing.T) {
	cfg := claudeDockerConfig(t)
	calls := withProviderCLIProbe(t, isolation.ProviderCLIProbe{Version: "2.1.221 (Claude Code)", Help: readProviderHelpFixture(t, "2.1.221")}, nil)
	d, err := New(cfg, nil)
	if err == nil {
		closeConstructedDaemon(d)
		t.Fatal("daemon started on an incompatible job image")
	}
	want := "the job image " + cfg.JobImage + " runs claude 2.1.221 (Claude Code), which does not accept the runner options --permission-prompts, --restricted; rebuild the job image (packaging/job-image) with a Claude Code release that supports them"
	if err.Error() != want {
		t.Fatalf("err = %q\nwant %q", err, want)
	}
	if len(*calls) != 1 {
		t.Fatalf("probe calls = %+v", *calls)
	}
	call := (*calls)[0]
	if call.image != cfg.JobImage || call.binary != "claude" || call.runnerID != "runner-1" || call.mapping.User() != "10001:10001" {
		t.Fatalf("probe call = %+v", call)
	}
	assertStartRefusalReleasedTheLock(t, cfg)
}

func TestNewRefusesWhenTheProviderCLIProbeFails(t *testing.T) {
	cfg := claudeDockerConfig(t)
	withProviderCLIProbe(t, isolation.ProviderCLIProbe{}, errors.New("probe claude in the job image: exit status 127: claude: not found"))
	d, err := New(cfg, nil)
	if err == nil {
		closeConstructedDaemon(d)
		t.Fatal("daemon started without a provider CLI probe")
	}
	if !strings.Contains(err.Error(), "verify the claude CLI in the job image "+cfg.JobImage) || !strings.Contains(err.Error(), "claude: not found") {
		t.Fatalf("err = %v", err)
	}
	assertStartRefusalReleasedTheLock(t, cfg)
}

func TestNewKeepsTheVerifiedProviderImagePair(t *testing.T) {
	cfg := claudeDockerConfig(t)
	cfg.ProviderBinaries = map[string]string{"claude": "/opt/tools/claude"}
	calls := withProviderCLIProbe(t, compatibleClaudeProbe(t), nil)
	d, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if version, ok := d.verifiedProviderCLIs[providerCLIKey{provider: "claude", image: cfg.JobImage}]; !ok || version != "2.1.299 (Claude Code)" {
		t.Fatalf("verified = %+v", d.verifiedProviderCLIs)
	}
	if len(*calls) != 1 || (*calls)[0].binary != "/opt/tools/claude" {
		t.Fatalf("probe calls = %+v", *calls)
	}
}

func TestNewDoesNotProbeProvidersWithoutAContractOrDirectIsolation(t *testing.T) {
	t.Run("codex only", func(t *testing.T) {
		installMappingDocker(t, rootlessMapOutput())
		cfg := enrolledRestrictedDockerConfig(t)
		calls := withProviderCLIProbe(t, isolation.ProviderCLIProbe{}, errors.New("must not run"))
		d, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer closeConstructedDaemon(d)
		if len(*calls) != 0 || len(d.verifiedProviderCLIs) != 0 {
			t.Fatalf("probe calls = %+v verified = %+v", *calls, d.verifiedProviderCLIs)
		}
	})
	t.Run("direct", func(t *testing.T) {
		cfg := enrolledDirectConfig(t)
		cfg.Provider = "claude"
		calls := withProviderCLIProbe(t, isolation.ProviderCLIProbe{}, errors.New("must not run"))
		d, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer closeConstructedDaemon(d)
		if len(*calls) != 0 {
			t.Fatalf("probe calls = %+v", *calls)
		}
	})
}

func TestDockerJobWithAnUnverifiedProviderCLIFailsBeforeAnyContainer(t *testing.T) {
	for name, verified := range map[string]map[providerCLIKey]string{
		"never verified": nil,
		"other image":    {{provider: "claude", image: "registry.example.test/job@sha256:" + strings.Repeat("b", 64)}: "2.1.299 (Claude Code)"},
		"other provider": {{provider: "codex", image: "registry.example.test/job@sha256:" + strings.Repeat("a", 64)}: "codex-cli 0.146.0"},
	} {
		t.Run(name, func(t *testing.T) {
			run := newOwnershipRun(t, testRootlessMapping)
			run.jr.d.verifiedProviderCLIs = verified
			run.execute(t)
			run.expectCalls(t, "")
			run.expectFinished(t, "failed", "agent_start_failed")
			if message := str(run.finished(t), "failure_message"); message != "job image provider CLI compatibility is unverified" {
				t.Fatalf("failure_message = %q", message)
			}
		})
	}
}

func TestDockerJobWithAVerifiedProviderCLIStarts(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	run.execute(t)
	run.expectCalls(t, "handoff:job start remove:a1 reclaim:job")
}

func TestProviderCLIVerifiedSkipsAdaptersWithoutAContract(t *testing.T) {
	d := &Daemon{}
	d.cfg.JobImage = "registry.example.test/job@sha256:" + strings.Repeat("a", 64)
	for name, want := range map[string]bool{"codex": true, "opencode": true, "claude": false} {
		adapter, err := provider.For(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.providerCLIVerified(adapter); got != want {
			t.Fatalf("%s verified = %v, want %v", name, got, want)
		}
	}
	d.verifiedProviderCLIs = map[providerCLIKey]string{{provider: "claude", image: d.cfg.JobImage}: "2.1.299 (Claude Code)"}
	if !d.providerCLIVerified(provider.NewClaude()) {
		t.Fatal("verified claude pair was not accepted")
	}
}

func TestVerifyDockerProviderCLIsProbesTheProposalAdapterOnce(t *testing.T) {
	cfg := enrolledRestrictedDockerConfig(t)
	cfg.WorkIntakeProposal = config.WorkIntakeProposalConfig{Enabled: true, Adapter: "claude", Model: "consultation-model"}
	calls := withProviderCLIProbe(t, compatibleClaudeProbe(t), nil)
	verified, err := verifyDockerProviderCLIs(cfg, "runner-1", testRootlessMapping, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].binary != "claude" || len(verified) != 1 {
		t.Fatalf("probe calls = %+v verified = %+v", *calls, verified)
	}
	cfg.Provider = "claude"
	*calls = nil
	if _, err := verifyDockerProviderCLIs(cfg, "runner-1", testRootlessMapping, nil); err != nil || len(*calls) != 1 {
		t.Fatalf("shared provider probed %d times: %v", len(*calls), err)
	}
	cfg.WorkIntakeProposal.Enabled = false
	cfg.Provider = "codex"
	*calls = nil
	if verified, err := verifyDockerProviderCLIs(cfg, "runner-1", testRootlessMapping, nil); err != nil || len(*calls) != 0 || len(verified) != 0 {
		t.Fatalf("disabled proposal adapter probed: calls %+v err %v", *calls, err)
	}
	cfg.WorkIntakeProposal.Enabled = true
	withProviderCLIProbe(t, isolation.ProviderCLIProbe{Version: "2.1.221 (Claude Code)", Help: readProviderHelpFixture(t, "2.1.221")}, nil)
	if _, err := verifyDockerProviderCLIs(cfg, "runner-1", testRootlessMapping, nil); err == nil || !strings.Contains(err.Error(), "--permission-prompts, --restricted") {
		t.Fatalf("incompatible proposal adapter err = %v", err)
	}
}
