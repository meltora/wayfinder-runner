package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/supervisor"
)

type workIntakeTestProcess struct {
	kills int
	done  chan struct{}
}

func (*workIntakeTestProcess) PID() int { return 42 }
func (process *workIntakeTestProcess) Wait() error {
	if process.done != nil {
		<-process.done
	}
	return nil
}
func (*workIntakeTestProcess) Interrupt() error { return nil }
func (process *workIntakeTestProcess) Kill() error {
	process.kills++
	if process.done != nil {
		close(process.done)
	}
	return nil
}

type workIntakeTestSupervisor struct {
	starts     int
	spec       supervisor.Spec
	process    *workIntakeTestProcess
	afterStart func(supervisor.Spec) error
}

type workIntakeTestPolicyLease struct {
	failures  chan error
	closes    int
	completes int
}

func (lease *workIntakeTestPolicyLease) Failures() <-chan error { return lease.failures }
func (lease *workIntakeTestPolicyLease) Complete(context.Context) error {
	lease.completes++
	select {
	case err := <-lease.failures:
		return err
	default:
		return nil
	}
}
func (lease *workIntakeTestPolicyLease) Close() error {
	lease.closes++
	return nil
}

func (*workIntakeTestSupervisor) Name() string    { return "test" }
func (*workIntakeTestSupervisor) Available() bool { return true }
func (value *workIntakeTestSupervisor) Start(_ context.Context, spec supervisor.Spec) (supervisor.Process, error) {
	value.starts++
	value.spec = spec
	if value.afterStart != nil {
		if err := value.afterStart(spec); err != nil {
			return nil, err
		}
	}
	return value.process, nil
}

func TestProviderOutputNormalizationIsInMemoryAndStrict(t *testing.T) {
	raw := []byte(`{"schema_version":1,"kind":"questions","questions":["  Cafe\u0301?  "]}`)
	proposal, complete, valid := readWorkIntakeProposalOutput("claude", "", raw)
	if !complete || !valid || proposal == nil || len(proposal.Questions) != 1 || proposal.Questions[0] != "Café?" {
		t.Fatalf("proposal=%+v complete=%v valid=%v", proposal, complete, valid)
	}
	if string(raw) != `{"schema_version":1,"kind":"questions","questions":["  Cafe\u0301?  "]}` {
		t.Fatal("raw provider output was mutated")
	}
}

func TestOpenCodeJSONEventExtractsOnlyStrictFinalText(t *testing.T) {
	raw := []byte(`{"type":"text","part":{"type":"text","text":"{\"schema_version\":1,\"kind\":\"questions\",\"questions\":[\"Who is affected?\"]}"}}`)
	proposal, complete, valid := readWorkIntakeProposalOutput("opencode", "", raw)
	if !complete || !valid || proposal == nil || len(proposal.Questions) != 1 || proposal.Questions[0] != "Who is affected?" {
		t.Fatalf("proposal=%+v complete=%v valid=%v", proposal, complete, valid)
	}
}

func TestProviderOutputClassificationAfterPaidCallFence(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "result.json")
	if err := os.WriteFile(output, []byte(`{"schema_version":1,"kind":"questions","questions":["valid question"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal, complete, valid := readWorkIntakeProposalOutput("codex", output, nil)
	if !complete || !valid || proposal.Kind != v1.WorkIntakeProposalQuestions {
		t.Fatalf("valid output proposal=%+v complete=%v valid=%v", proposal, complete, valid)
	}
	if err := os.WriteFile(output, []byte(`{"schema_version":1,"kind":"questions","questions":["bad\tquestion"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal, complete, valid = readWorkIntakeProposalOutput("codex", output, nil)
	if !complete || valid || proposal != nil {
		t.Fatalf("complete invalid output proposal=%+v complete=%v valid=%v", proposal, complete, valid)
	}
	outcome := classifyWorkIntakeProposalOutput("codex", output, &boundedMemoryWriter{maximum: 1024})
	if outcome.Kind != v1.WorkIntakeProposalPermanentFailure || outcome.Code != v1.WorkIntakeProposalInvalidOutput {
		t.Fatalf("complete invalid output outcome=%+v", outcome)
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	proposal, complete, valid = readWorkIntakeProposalOutput("codex", output, nil)
	if complete || valid || proposal != nil {
		t.Fatalf("lost output proposal=%+v complete=%v valid=%v", proposal, complete, valid)
	}
	outcome = classifyWorkIntakeProposalOutput("codex", output, &boundedMemoryWriter{maximum: 1024})
	if outcome.Kind != v1.WorkIntakeProposalUncertain || outcome.Code != v1.WorkIntakeProposalGenerationUncertain {
		t.Fatalf("lost/incomplete output outcome=%+v", outcome)
	}
}

func TestBoundedProviderOutputNeverRetainsBeyondLimit(t *testing.T) {
	writer := &boundedMemoryWriter{maximum: 8}
	input := strings.Repeat("x", 32)
	if count, err := writer.Write([]byte(input)); err != nil || count != len(input) {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if !writer.overflow || len(writer.Bytes()) != 8 {
		t.Fatalf("overflow=%v retained=%d", writer.overflow, len(writer.Bytes()))
	}
}

func TestProviderGenerationFailsBeforeStartWithoutHardenedDockerAttestation(t *testing.T) {
	generator := &providerWorkIntakeProposalGenerator{
		cfg: config.Config{Isolation: "docker", JobNetwork: "restricted"},
		probeDocker: func(context.Context) isolation.DockerInfo {
			return isolation.DockerInfo{Available: true}
		},
	}
	outcome := generator.Generate(context.Background(), v1.WorkIntakeProposalOperation{})
	if outcome.ProviderStarted || outcome.Outcome.Kind != v1.WorkIntakeProposalRetryableFailure || outcome.Outcome.Code != v1.WorkIntakeProposalProviderUnavailable {
		t.Fatalf("rootful Docker outcome=%+v", outcome)
	}
	generator.probeDocker = func(context.Context) isolation.DockerInfo { return isolation.DockerInfo{} }
	outcome = generator.Generate(context.Background(), v1.WorkIntakeProposalOperation{})
	if outcome.ProviderStarted || outcome.Outcome.Kind != v1.WorkIntakeProposalRetryableFailure || outcome.Outcome.Code != v1.WorkIntakeProposalProviderUnavailable {
		t.Fatalf("missing Docker outcome=%+v", outcome)
	}
}

func TestProviderGenerationRejectsRestrictedNetworkReplacementBeforeStart(t *testing.T) {
	attestation := isolation.DockerNetworkAttestation{
		ID: strings.Repeat("a", 64), Name: "provider-egress", PolicyID: "consultation-v1",
		Driver: "bridge", Scope: "local",
	}
	generator := &providerWorkIntakeProposalGenerator{
		cfg: config.Config{Isolation: "docker", JobNetwork: "restricted", StateDir: t.TempDir()},
		probeDocker: func(context.Context) isolation.DockerInfo {
			return isolation.DockerInfo{Available: true, Rootless: true}
		},
		networkAttestation: attestation,
		attestNetwork: func(context.Context, isolation.DockerNetworkAttestation) error {
			return os.ErrNotExist
		},
	}
	result := generator.Generate(context.Background(), v1.WorkIntakeProposalOperation{})
	if result.ProviderStarted || result.Outcome.Kind != v1.WorkIntakeProposalRetryableFailure {
		t.Fatalf("replacement network reached provider start: %+v", result)
	}
}

func TestProviderGenerationFailsClosedOnEffectivePolicyDriftBeforeAndAfterStart(t *testing.T) {
	binDir := t.TempDir()
	dockerPath := filepath.Join(binDir, "docker")
	if err := os.WriteFile(dockerPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	attestation := isolation.DockerNetworkAttestation{
		ID: strings.Repeat("a", 64), Name: "provider-egress", PolicyID: "consultation-v1",
		Driver: "bridge", Scope: "local",
	}
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	cfg.JobImage = "registry.example.com/runner/job@sha256:" + strings.Repeat("b", 64)
	cfg.JobNetwork = "restricted"
	cfg.JobNetworkName = attestation.Name
	cfg.EgressPolicyID = attestation.PolicyID
	cfg.WorkIntakeProposal = config.WorkIntakeProposalConfig{Enabled: true, Adapter: "codex", Model: "consultation-model"}
	process := &workIntakeTestProcess{}
	sup := &workIntakeTestSupervisor{process: process}
	containerRemovals := 0
	generator := &providerWorkIntakeProposalGenerator{
		cfg: cfg, runnerID: "runner-1", supervisor: sup,
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
		removeContainer: func(context.Context, string, string) error {
			containerRemovals++
			return nil
		},
		userMapping: testRootlessMapping,
		ownership:   &recordingOwnership{log: &orderLog{}},
	}
	digest, err := generator.ModelExecutionConfigDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operation := v1.WorkIntakeProposalOperation{
		WorkIntakeProposalAuthority: v1.WorkIntakeProposalAuthority{
			WorkIntakeProposalRequestID: "request-1", WorkIntakeProposalEffectID: "effect-1",
			WorkIntakeProposalAttemptID: "attempt-1", Adapter: "codex", Model: "consultation-model",
			ModelExecutionConfigDigest: digest,
		},
		Kind:           v1.WorkIntakeProposalQuestions,
		Intent:         v1.WorkIntakeProposalIntent{Problem: "Planning is inconsistent", AffectedUser: "Teams", DesiredOutcome: "A reviewable draft"},
		Clarifications: []v1.WorkIntakeProposalClarification{},
	}
	generator.acquireEffectivePolicy = func(
		context.Context, egresspolicy.Binding, time.Duration,
	) (effectivePolicyLease, error) {
		return nil, errors.New("wrong policy is reachable")
	}
	result := generator.Generate(context.Background(), operation)
	if result.ProviderStarted || sup.starts != 0 {
		t.Fatalf("pre-start policy drift reached provider: result=%+v starts=%d", result, sup.starts)
	}

	process = &workIntakeTestProcess{done: make(chan struct{})}
	lease := &workIntakeTestPolicyLease{failures: make(chan error, 1)}
	sup = &workIntakeTestSupervisor{
		process: process,
		afterStart: func(supervisor.Spec) error {
			lease.failures <- errors.New("effective policy lease revoked after start")
			return nil
		},
	}
	generator.supervisor = sup
	generator.acquireEffectivePolicy = func(
		context.Context, egresspolicy.Binding, time.Duration,
	) (effectivePolicyLease, error) {
		return lease, nil
	}
	result = generator.Generate(context.Background(), operation)
	if !result.ProviderStarted || result.Outcome.Kind != v1.WorkIntakeProposalUncertain ||
		sup.starts != 1 || process.kills != 1 || lease.closes != 1 || lease.completes != 0 || containerRemovals != 1 {
		t.Fatalf("result=%+v starts=%d kills=%d closes=%d completes=%d removals=%d", result, sup.starts, process.kills, lease.closes, lease.completes, containerRemovals)
	}
	joined := strings.Join(sup.spec.Args, " ")
	if !strings.Contains(joined, "--network "+attestation.ID) || !strings.Contains(joined, "--log-driver none") {
		t.Fatalf("provider start did not pin network/log policy: %s", joined)
	}

	process = &workIntakeTestProcess{}
	lease = &workIntakeTestPolicyLease{failures: make(chan error)}
	sup = &workIntakeTestSupervisor{
		process: process,
		afterStart: func(spec supervisor.Spec) error {
			for index, argument := range spec.Args {
				if argument == "--output-last-message" && index+1 < len(spec.Args) {
					return os.WriteFile(
						spec.Args[index+1],
						[]byte(`{"schema_version":1,"kind":"questions","questions":["Who is affected?"]}`),
						0o600,
					)
				}
			}
			return errors.New("Codex output path not found")
		},
	}
	generator.supervisor = sup
	generator.acquireEffectivePolicy = func(
		context.Context, egresspolicy.Binding, time.Duration,
	) (effectivePolicyLease, error) {
		return lease, nil
	}
	result = generator.Generate(context.Background(), operation)
	if !result.ProviderStarted || result.Outcome.Kind != v1.WorkIntakeProposalSucceeded ||
		result.Outcome.Proposal == nil || sup.starts != 1 || process.kills != 0 || lease.closes != 1 || lease.completes != 1 || containerRemovals != 2 {
		t.Fatalf("valid policy result=%+v starts=%d kills=%d closes=%d completes=%d removals=%d", result, sup.starts, process.kills, lease.closes, lease.completes, containerRemovals)
	}
}

func TestModelExecutionConfigDigestBindsEveryEffectiveSecurityControl(t *testing.T) {
	attestation := isolation.DockerNetworkAttestation{
		ID: strings.Repeat("a", 64), Name: "provider-egress", PolicyID: "consultation-v1",
		Driver: "bridge", Scope: "local",
	}
	generator := &providerWorkIntakeProposalGenerator{
		runnerID: "runner-1",
		cfg: config.Config{
			JobImage:    "registry.example.com/runner/job@sha256:" + strings.Repeat("b", 64),
			JobMemoryMB: 4096, JobCPUs: 2, JobPIDs: 512,
			WorkIntakeProposal: config.WorkIntakeProposalConfig{Adapter: "codex", Model: "consultation-model"},
		},
		networkAttestation: attestation,
		effectivePolicy: egresspolicy.Binding{
			NetworkID: attestation.ID, PolicyID: attestation.PolicyID, EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64),
		},
		probeDocker: func(context.Context) isolation.DockerInfo {
			return isolation.DockerInfo{Available: true, Rootless: true, ServerVersion: "26.1.0"}
		},
		runVersion:  func(context.Context, string) (string, error) { return "codex 1.2.3", nil },
		userMapping: testRootlessMapping,
	}
	spec := supervisor.Spec{
		Command: "/opt/bin/codex",
		Args:    []string{"exec", "--ephemeral", "--disable", "shell_tool", "--output-schema", "/private/config/schema.json", "-"},
		Env:     []string{"OPENCODE_DISABLE_SHARE=1"},
	}
	digest, err := generator.modelExecutionConfigDigestForSpec(context.Background(), "/private", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) {
		t.Fatalf("execution config digest = %s", digest)
	}
	repeated, err := generator.modelExecutionConfigDigestForSpec(context.Background(), "/private", spec)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != digest {
		t.Fatalf("execution config digest is not deterministic: %s != %s", repeated, digest)
	}
	mutations := map[string]func(*providerWorkIntakeProposalGenerator, *supervisor.Spec){
		"adapter version": func(g *providerWorkIntakeProposalGenerator, _ *supervisor.Spec) {
			g.runVersion = func(context.Context, string) (string, error) { return "codex 1.2.4", nil }
		},
		"provider flags": func(_ *providerWorkIntakeProposalGenerator, s *supervisor.Spec) {
			s.Args = append([]string(nil), s.Args...)
			s.Args[3] = "unified_exec"
		},
		"provider environment": func(_ *providerWorkIntakeProposalGenerator, s *supervisor.Spec) {
			s.Env = append(s.Env, "OPENCODE_DISABLE_AUTOUPDATE=1")
		},
		"image digest": func(g *providerWorkIntakeProposalGenerator, _ *supervisor.Spec) {
			g.cfg.JobImage = "registry.example.com/runner/job@sha256:" + strings.Repeat("c", 64)
		},
		"network identity": func(g *providerWorkIntakeProposalGenerator, _ *supervisor.Spec) {
			g.networkAttestation.ID = strings.Repeat("d", 64)
		},
		"effective policy rules": func(g *providerWorkIntakeProposalGenerator, _ *supervisor.Spec) {
			g.effectivePolicy.RulesDigest = strings.Repeat("e", 64)
		},
		"job user mapping": func(g *providerWorkIntakeProposalGenerator, _ *supervisor.Spec) {
			g.userMapping = testIdentityMapping
		},
		"Docker isolation mode": func(g *providerWorkIntakeProposalGenerator, _ *supervisor.Spec) {
			g.probeDocker = func(context.Context) isolation.DockerInfo {
				return isolation.DockerInfo{Available: true, UsernsRemap: true, ServerVersion: "26.1.0"}
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := *generator
			candidateSpec := spec
			mutate(&candidate, &candidateSpec)
			changed, err := candidate.modelExecutionConfigDigestForSpec(context.Background(), "/private", candidateSpec)
			if err != nil {
				t.Fatal(err)
			}
			if changed == digest {
				t.Fatalf("%s drift did not change execution config digest", name)
			}
		})
	}
}

func TestCleanupStaleWorkIntakeProposalExecutionsIsStrictlyScoped(t *testing.T) {
	stateDir := t.TempDir()
	root := filepath.Join(stateDir, "work-intake-proposals")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "execution-123456")
	preserved := filepath.Join(root, "execution-654321")
	unrelated := filepath.Join(root, "other-state")
	malformed := filepath.Join(root, "execution-")
	for _, directory := range []string{stale, preserved, unrelated, malformed} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "sensitive.json"), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupStaleWorkIntakeProposalExecutions(stateDir, map[string]struct{}{
		filepath.Base(preserved): {},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale execution still exists: %v", err)
	}
	for _, expected := range []string{preserved, unrelated, malformed} {
		if _, err := os.Lstat(expected); err != nil {
			t.Fatalf("out-of-scope path %q was removed: %v", expected, err)
		}
	}
}

func TestCleanupStaleWorkIntakeProposalExecutionsRejectsBroadOrUnresolvedRoots(t *testing.T) {
	uncleanBase := t.TempDir()
	for name, stateDir := range map[string]string{
		"relative":        "relative-state",
		"filesystem root": string(filepath.Separator),
		"unclean":         uncleanBase + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(uncleanBase),
	} {
		t.Run(name, func(t *testing.T) {
			if err := cleanupStaleWorkIntakeProposalExecutions(stateDir, nil); err == nil {
				t.Fatal("unsafe cleanup root was accepted")
			}
		})
	}
}

func TestCleanupStaleWorkIntakeProposalExecutionsNeverFollowsSymlinks(t *testing.T) {
	stateDir := t.TempDir()
	external := t.TempDir()
	marker := filepath.Join(external, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(stateDir, "work-intake-proposals")
	if err := os.Symlink(external, root); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStaleWorkIntakeProposalExecutions(stateDir, nil); err == nil {
		t.Fatal("symlinked work-intake root was accepted")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "keep" {
		t.Fatalf("external target changed: %q err=%v", raw, err)
	}

	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	childLink := filepath.Join(root, "execution-123456")
	if err := os.Symlink(external, childLink); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStaleWorkIntakeProposalExecutions(stateDir, nil); err == nil {
		t.Fatal("symlinked execution directory was accepted")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "keep" {
		t.Fatalf("external child target changed: %q err=%v", raw, err)
	}
}

func TestCleanupStaleWorkIntakeProposalExecutionsRemovesNestedSymlinkWithoutFollowingIt(t *testing.T) {
	stateDir := t.TempDir()
	root := filepath.Join(stateDir, "work-intake-proposals")
	execution := filepath.Join(root, "execution-123456")
	external := t.TempDir()
	marker := filepath.Join(external, "keep.txt")
	if err := os.MkdirAll(execution, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(execution, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStaleWorkIntakeProposalExecutions(stateDir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(execution); !os.IsNotExist(err) {
		t.Fatalf("stale execution still exists: %v", err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "keep" {
		t.Fatalf("nested symlink target changed: %q err=%v", raw, err)
	}
}

func TestWorkIntakeDraftSerializesEmptyOrMissingNonGoalsAsEmptyArray(t *testing.T) {
	base := `{"schema_version":1,"kind":"draft","draft":{"problem":"Planning is inconsistent","affected_user":"Delivery teams","desired_outcome":"A reviewable draft","title":"Make planning consistent","description":"Create a consistent proposal flow.","acceptance_criteria":["A proposal can be reviewed"],%s"risk":"medium","work_type":"feature"}}`
	for _, nonGoals := range []string{`"non_goals":[],`, ``} {
		proposal, ok := decodeWorkIntakeProposalCandidate([]byte(fmt.Sprintf(base, nonGoals)))
		if !ok || proposal == nil {
			t.Fatalf("proposal with %q was rejected", nonGoals)
		}
		raw, err := json.Marshal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"non_goals":[]`) {
			t.Fatalf("proposal with %q serialized as %s", nonGoals, raw)
		}
	}
}
