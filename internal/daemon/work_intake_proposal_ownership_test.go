package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/supervisor"
)

type workIntakeOwnershipHarness struct {
	generator *providerWorkIntakeProposalGenerator
	log       *orderLog
	ownership *recordingOwnership
	sup       *workIntakeTestSupervisor
	operation v1.WorkIntakeProposalOperation
	removeErr error
}

func newWorkIntakeOwnershipHarness(t *testing.T) *workIntakeOwnershipHarness {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	installProviderCredentials(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	attestation := isolation.DockerNetworkAttestation{
		ID: strings.Repeat("a", 64), Name: "provider-egress", PolicyID: "consultation-v1", Driver: "bridge", Scope: "local",
	}
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	cfg.JobImage = "registry.example.com/runner/job@sha256:" + strings.Repeat("b", 64)
	cfg.JobNetwork = "restricted"
	cfg.JobNetworkName = attestation.Name
	cfg.EgressPolicyID = attestation.PolicyID
	cfg.WorkIntakeProposal = config.WorkIntakeProposalConfig{Enabled: true, Adapter: "codex", Model: "consultation-model"}
	h := &workIntakeOwnershipHarness{log: &orderLog{}}
	h.ownership = &recordingOwnership{log: h.log}
	h.sup = &workIntakeTestSupervisor{process: &workIntakeTestProcess{}, afterStart: func(spec supervisor.Spec) error {
		h.log.add("start")
		for index, argument := range spec.Args {
			if argument == "--output-last-message" && index+1 < len(spec.Args) {
				return os.WriteFile(spec.Args[index+1], []byte(`{"schema_version":1,"kind":"questions","questions":["Who is affected?"]}`), 0o600)
			}
		}
		return errors.New("Codex output path not found")
	}}
	h.generator = &providerWorkIntakeProposalGenerator{
		cfg: cfg, runnerID: "runner-1", supervisor: h.sup,
		probeDocker: func(context.Context) isolation.DockerInfo {
			return isolation.DockerInfo{Available: true, Rootless: true}
		},
		networkAttestation: attestation,
		attestNetwork:      func(context.Context, isolation.DockerNetworkAttestation) error { return nil },
		runVersion:         func(context.Context, string) (string, error) { return "codex 1.2.3", nil },
		effectivePolicy: egresspolicy.Binding{
			NetworkID: attestation.ID, PolicyID: attestation.PolicyID, EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64),
		},
		attestEffectivePolicy: func(context.Context, egresspolicy.Binding) error { return nil },
		acquireEffectivePolicy: func(context.Context, egresspolicy.Binding, time.Duration) (effectivePolicyLease, error) {
			return &workIntakeTestPolicyLease{failures: make(chan error)}, nil
		},
		removeContainer: func(_ context.Context, _, attemptID string) error {
			h.log.add("remove:" + attemptID)
			return h.removeErr
		},
		userMapping: testRootlessMapping,
		ownership:   h.ownership,
	}
	digest, err := h.generator.ModelExecutionConfigDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.operation = v1.WorkIntakeProposalOperation{
		WorkIntakeProposalAuthority: v1.WorkIntakeProposalAuthority{
			WorkIntakeProposalRequestID: "request-1", WorkIntakeProposalEffectID: "effect-1",
			WorkIntakeProposalAttemptID: "attempt-1", Adapter: "codex", Model: "consultation-model",
			ModelExecutionConfigDigest: digest,
		},
		Kind:           v1.WorkIntakeProposalQuestions,
		Intent:         v1.WorkIntakeProposalIntent{Problem: "Planning is inconsistent", AffectedUser: "Teams", DesiredOutcome: "A reviewable draft"},
		Clarifications: []v1.WorkIntakeProposalClarification{},
	}
	return h
}

func TestWorkIntakeProposalHandsOffAndReclaimsBeforeReadingTheResult(t *testing.T) {
	h := newWorkIntakeOwnershipHarness(t)
	result := h.generator.Generate(context.Background(), h.operation)
	if result.Outcome.Kind != v1.WorkIntakeProposalSucceeded {
		t.Fatalf("result = %+v (calls %s)", result, h.log)
	}
	if got := h.log.String(); got != "handoff:proposal start remove:attempt-1 reclaim:proposal" {
		t.Fatalf("calls = %q", got)
	}
	request := h.ownership.handOffs[0]
	executions := filepath.Join(h.generator.cfg.StateDir, workIntakeProposalExecutionRootName)
	if request.AttemptID != "attempt-1" || len(request.Workspace) != 1 || !strings.HasPrefix(request.Workspace[0], filepath.Join(executions, "execution-")) ||
		len(request.Private) != 1 || !strings.HasPrefix(request.Private[0], filepath.Join(h.generator.cfg.StateDir, "job-homes", "home-")) {
		t.Fatalf("hand-off request = %+v", request)
	}
	if users := argumentsAfter(h.sup.spec.Args, "--user"); !slices.Equal(users, []string{"10001:10001"}) {
		t.Fatalf("--user values = %v", users)
	}
	if entries, err := os.ReadDir(executions); err != nil || len(entries) != 0 {
		t.Fatalf("execution roots left: %v %v", entries, err)
	}
}

func TestWorkIntakeProposalReclaimsOnStartFailureAndHandOffFailure(t *testing.T) {
	h := newWorkIntakeOwnershipHarness(t)
	h.sup.afterStart = func(supervisor.Spec) error {
		h.log.add("start")
		return errors.New("docker run failed")
	}
	if result := h.generator.Generate(context.Background(), h.operation); result.ProviderStarted {
		t.Fatalf("result = %+v", result)
	}
	if got := h.log.String(); got != "handoff:proposal start remove:attempt-1 reclaim:proposal" {
		t.Fatalf("start failure calls = %q", got)
	}

	h = newWorkIntakeOwnershipHarness(t)
	h.ownership.handOffErr = errors.New("chown failed")
	if result := h.generator.Generate(context.Background(), h.operation); result.ProviderStarted || h.sup.starts != 0 {
		t.Fatalf("result = %+v starts = %d", result, h.sup.starts)
	}
	if got := h.log.String(); got != "handoff:proposal remove:attempt-1 reclaim:proposal" {
		t.Fatalf("hand-off failure calls = %q", got)
	}
}

func TestWorkIntakeProposalIsUncertainWhenOutputCannotBeReowned(t *testing.T) {
	for name, configure := range map[string]func(*workIntakeOwnershipHarness){
		"reclaim failure":           func(h *workIntakeOwnershipHarness) { h.ownership.reclaimErr = errors.New("chown failed") },
		"container removal failure": func(h *workIntakeOwnershipHarness) { h.removeErr = errors.New("daemon unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newWorkIntakeOwnershipHarness(t)
			configure(h)
			result := h.generator.Generate(context.Background(), h.operation)
			if !result.ProviderStarted || result.Outcome.Kind != v1.WorkIntakeProposalUncertain {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestWorkIntakeProposalRefusesRootlessWithoutAnOwnershipHelper(t *testing.T) {
	h := newWorkIntakeOwnershipHarness(t)
	h.generator.ownership = nil
	if result := h.generator.Generate(context.Background(), h.operation); result.ProviderStarted || h.sup.starts != 0 {
		t.Fatalf("result = %+v starts = %d", result, h.sup.starts)
	}
}

func TestModelExecutionConfigDigestRequiresAResolvedJobUser(t *testing.T) {
	h := newWorkIntakeOwnershipHarness(t)
	rootless, err := h.generator.ModelExecutionConfigDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.generator.userMapping = testIdentityMapping
	identity, err := h.generator.ModelExecutionConfigDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity == rootless {
		t.Fatal("mapping mode change did not change the execution config digest")
	}
	h.generator.userMapping = isolation.UserMapping{}
	if _, err := h.generator.ModelExecutionConfigDigest(context.Background()); err == nil {
		t.Fatal("digest computed without a resolved job user")
	}
}
