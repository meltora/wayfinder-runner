package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/provider"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/supervisor"
	"github.com/example/app-runner/internal/validation"
	"golang.org/x/text/unicode/norm"
)

const workIntakeProposalOutputSchema = `{"oneOf":[{"type":"object","additionalProperties":false,"required":["schema_version","kind","questions"],"properties":{"schema_version":{"const":1},"kind":{"const":"questions"},"questions":{"type":"array","maxItems":4,"items":{"type":"string"}}}},{"type":"object","additionalProperties":false,"required":["schema_version","kind","draft"],"properties":{"schema_version":{"const":1},"kind":{"const":"draft"},"draft":{"type":"object","additionalProperties":false,"required":["problem","affected_user","desired_outcome","title","description","acceptance_criteria","non_goals","risk","work_type"],"properties":{"problem":{"type":"string"},"affected_user":{"type":"string"},"desired_outcome":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},"acceptance_criteria":{"type":"array","items":{"type":"string"}},"non_goals":{"type":"array","items":{"type":"string"}},"risk":{"enum":["low","medium","high","critical"]},"work_type":{"enum":["feature","bug","maintenance","incident_follow_up","other"]}}}}}]}`

type promptOnlySpecBuilder interface {
	BuildPromptOnlySpec(provider.PromptOnlyParams) (supervisor.Spec, error)
}

type effectivePolicyLease interface {
	Failures() <-chan error
	Complete(context.Context) error
	Close() error
}

const workIntakeProposalPolicyLeaseDuration = 35 * time.Second

type providerWorkIntakeProposalGenerator struct {
	cfg                    config.Config
	runnerID               string
	supervisor             supervisor.Supervisor
	probeDocker            func(context.Context) isolation.DockerInfo
	networkAttestation     isolation.DockerNetworkAttestation
	attestNetwork          func(context.Context, isolation.DockerNetworkAttestation) error
	runVersion             func(context.Context, string) (string, error)
	effectivePolicy        egresspolicy.Binding
	attestEffectivePolicy  func(context.Context, egresspolicy.Binding) error
	acquireEffectivePolicy func(context.Context, egresspolicy.Binding, time.Duration) (effectivePolicyLease, error)
	removeContainer        func(context.Context, string, string) error
	userMapping            isolation.UserMapping
	ownership              isolation.Ownership
}

func newProviderWorkIntakeProposalGenerator(
	cfg config.Config,
	runnerID string,
	sup supervisor.Supervisor,
	networkAttestation isolation.DockerNetworkAttestation,
	effectivePolicy egresspolicy.Binding,
	userMapping isolation.UserMapping,
	ownership isolation.Ownership,
) *providerWorkIntakeProposalGenerator {
	generator := &providerWorkIntakeProposalGenerator{
		cfg: cfg, runnerID: runnerID, supervisor: sup, probeDocker: isolation.ProbeDocker,
		userMapping: userMapping, ownership: ownership,
		networkAttestation: networkAttestation, attestNetwork: isolation.VerifyDockerNetworkAttestation,
		effectivePolicy: effectivePolicy,
	}
	policyClient := egresspolicy.NewClient(cfg.EgressPolicyVerifierSocket)
	generator.attestEffectivePolicy = func(ctx context.Context, expected egresspolicy.Binding) error {
		current, err := policyClient.Attest(ctx, networkAttestation.ID, networkAttestation.Name, networkAttestation.PolicyID)
		if err != nil {
			return err
		}
		if current != expected {
			return fmt.Errorf("effective restricted egress policy identity changed")
		}
		return nil
	}
	generator.acquireEffectivePolicy = func(
		ctx context.Context,
		expected egresspolicy.Binding,
		duration time.Duration,
	) (effectivePolicyLease, error) {
		lease, err := policyClient.Acquire(ctx, networkAttestation.ID, networkAttestation.Name, networkAttestation.PolicyID, duration)
		if err != nil {
			return nil, err
		}
		if lease.Binding != expected {
			_ = lease.Close()
			return nil, fmt.Errorf("effective restricted egress policy identity changed")
		}
		return lease, nil
	}
	generator.removeContainer = isolation.RemoveJobContainer
	return generator
}

func (generator *providerWorkIntakeProposalGenerator) Generate(
	ctx context.Context,
	operation v1.WorkIntakeProposalOperation,
) workIntakeProposalGenerationResult {
	if generator.cfg.Isolation != "docker" || generator.cfg.JobNetwork != "restricted" ||
		generator.probeDocker == nil {
		return workIntakeProposalPreStartFailure()
	}
	dockerInfo := generator.probeDocker(ctx)
	if !dockerInfo.Available || (!dockerInfo.Rootless && !dockerInfo.UsernsRemap) {
		return workIntakeProposalPreStartFailure()
	}
	if generator.userMapping.Validate() != nil ||
		(generator.userMapping.RequiresOwnershipHandOff() && generator.ownership == nil) {
		return workIntakeProposalPreStartFailure()
	}
	root, err := generator.preparePrivateExecutionRoot()
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	defer func() { _ = removeRunnerTree(root) }()
	workspace := filepath.Join(root, "workspace")
	privateConfig := filepath.Join(root, "config")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return workIntakeProposalPreStartFailure()
	}
	if err := os.Mkdir(privateConfig, 0o700); err != nil {
		return workIntakeProposalPreStartFailure()
	}
	schemaPath := filepath.Join(privateConfig, "schema.json")
	if err := safeio.AtomicWriteFile(privateConfig, "schema.json", []byte(workIntakeProposalOutputSchema), 0o600); err != nil {
		return workIntakeProposalPreStartFailure()
	}
	prompt, err := buildWorkIntakeProposalPrompt(operation)
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	adapter, err := provider.For(generator.cfg.WorkIntakeProposal.Adapter)
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	builder, ok := adapter.(promptOnlySpecBuilder)
	if !ok {
		return workIntakeProposalPreStartFailure()
	}
	outputPath := filepath.Join(privateConfig, "result.json")
	spec, err := builder.BuildPromptOnlySpec(provider.PromptOnlyParams{
		ExecutionID: operation.WorkIntakeProposalEffectID,
		WorkDir:     workspace, ConfigDir: privateConfig,
		Prompt:     prompt,
		SchemaPath: schemaPath, OutputPath: outputPath,
		Model:      operation.Model,
		BinaryPath: generator.cfg.ProviderBinaries[operation.Adapter],
	})
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	executionConfigDigest, err := generator.modelExecutionConfigDigestForSpec(ctx, root, spec)
	if err != nil || executionConfigDigest != operation.ModelExecutionConfigDigest {
		return workIntakeProposalPreStartFailure()
	}
	jobHome, err := isolation.PrepareHome(
		generator.cfg.StateDir,
		operation.WorkIntakeProposalRequestID,
		operation.WorkIntakeProposalAttemptID,
		operation.Adapter,
		generator.cfg.AgentEnvironment(operation.Adapter),
	)
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	defer func() { _ = isolation.CleanupHome(jobHome) }()
	isolator, err := isolation.Select("docker", false)
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	policy := generator.workIntakeProposalIsolationPolicy(
		root, jobHome, operation.WorkIntakeProposalAttemptID, operation.Adapter,
		provider.ContainerEnvironment(spec.Env),
	)
	spec.Command, spec.Args, err = isolator.Wrap(spec.Command, spec.Args, policy)
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	stdout := &boundedMemoryWriter{maximum: 64 << 10}
	stderr := &boundedMemoryWriter{maximum: 16 << 10}
	spec.Stdout = stdout
	spec.Stderr = stderr
	// Re-attest immediately before Docker resolves the immutable network ID. A
	// same-name delete/recreate cannot redirect this start because Wrap uses the
	// attested ID rather than the reusable configured name.
	if generator.attestDockerNetworkIdentity(ctx) != nil || generator.acquireEffectivePolicy == nil ||
		generator.effectivePolicy.NetworkID == "" {
		return workIntakeProposalPreStartFailure()
	}
	policyLease, err := generator.acquireEffectivePolicy(ctx, generator.effectivePolicy, workIntakeProposalPolicyLeaseDuration)
	if err != nil {
		return workIntakeProposalPreStartFailure()
	}
	defer func() { _ = policyLease.Close() }()
	ownership, err := handOffOwnership(ctx, generator.ownership, generator.removeContainer, isolation.OwnershipRequest{
		RunnerID: generator.runnerID, AttemptID: operation.WorkIntakeProposalAttemptID, Purpose: "proposal",
		Workspace: []string{root}, Private: []string{jobHome},
	})
	if err != nil {
		_ = ownership.Release()
		return workIntakeProposalPreStartFailure()
	}
	process, err := generator.supervisor.Start(ctx, spec)
	if err != nil {
		_ = ownership.Release()
		return workIntakeProposalPreStartFailure()
	}
	if generator.attestDockerNetworkIdentity(ctx) != nil {
		_ = process.Kill()
		_ = process.Wait()
		_ = ownership.Release()
		return workIntakeProposalUncertainResult()
	}
	completed := generator.waitWithEffectivePolicyLease(process, policyLease)
	if ownership.Release() != nil || !completed {
		return workIntakeProposalUncertainResult()
	}
	return workIntakeProposalGenerationResult{
		Outcome: classifyWorkIntakeProposalOutput(operation.Adapter, outputPath, stdout), ProviderStarted: true,
	}
}

func (generator *providerWorkIntakeProposalGenerator) attestDockerNetworkIdentity(ctx context.Context) error {
	if generator.attestNetwork == nil || generator.networkAttestation.ID == "" {
		return fmt.Errorf("restricted Docker network attestation is unavailable")
	}
	return generator.attestNetwork(ctx, generator.networkAttestation)
}

func (generator *providerWorkIntakeProposalGenerator) waitWithEffectivePolicyLease(
	process supervisor.Process,
	policyLease effectivePolicyLease,
) bool {
	finished := make(chan struct{})
	go func() {
		_ = process.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		completeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return policyLease.Complete(completeCtx) == nil
	case <-policyLease.Failures():
		_ = process.Kill()
		<-finished
		return false
	}
}

func workIntakeProposalUncertainResult() workIntakeProposalGenerationResult {
	return workIntakeProposalGenerationResult{
		Outcome: v1.WorkIntakeProposalOutcome{
			Kind: v1.WorkIntakeProposalUncertain, Code: v1.WorkIntakeProposalGenerationUncertain,
		},
		ProviderStarted: true,
	}
}

func classifyWorkIntakeProposalOutput(adapter, outputPath string, stdout *boundedMemoryWriter) v1.WorkIntakeProposalOutcome {
	proposal, complete, valid := readWorkIntakeProposalOutput(adapter, outputPath, stdout.Bytes())
	if valid {
		return v1.WorkIntakeProposalOutcome{Kind: v1.WorkIntakeProposalSucceeded, Proposal: proposal}
	}
	if complete && (adapter == "codex" || !stdout.overflow) {
		return v1.WorkIntakeProposalOutcome{
			Kind: v1.WorkIntakeProposalPermanentFailure, Code: v1.WorkIntakeProposalInvalidOutput,
		}
	}
	return v1.WorkIntakeProposalOutcome{
		Kind: v1.WorkIntakeProposalUncertain, Code: v1.WorkIntakeProposalGenerationUncertain,
	}
}

func buildWorkIntakeProposalPrompt(operation v1.WorkIntakeProposalOperation) (string, error) {
	input, err := json.Marshal(struct {
		Kind           v1.WorkIntakeProposalKind            `json:"kind"`
		Intent         v1.WorkIntakeProposalIntent          `json:"intent"`
		Clarifications []v1.WorkIntakeProposalClarification `json:"clarifications"`
	}{operation.Kind, operation.Intent, operation.Clarifications})
	if err != nil {
		return "", fmt.Errorf("encode work intake proposal prompt input: %w", err)
	}
	return fmt.Sprintf(workIntakeProposalPromptTemplate, string(input)), nil
}

func (generator *providerWorkIntakeProposalGenerator) preparePrivateExecutionRoot() (string, error) {
	parent := filepath.Join(generator.cfg.StateDir, workIntakeProposalExecutionRootName)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	root, err := os.MkdirTemp(parent, "execution-")
	if err != nil {
		return "", err
	}
	if err := restrictDirectoryToOwner(parent, filepath.Base(root)); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	return root, nil
}

func restrictDirectoryToOwner(parent, name string) error {
	scoped, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer func() { _ = scoped.Close() }()
	return scoped.Chmod(name, 0o700)
}

func workIntakeProposalPreStartFailure() workIntakeProposalGenerationResult {
	return workIntakeProposalGenerationResult{Outcome: v1.WorkIntakeProposalOutcome{
		Kind: v1.WorkIntakeProposalRetryableFailure, Code: v1.WorkIntakeProposalProviderUnavailable,
	}}
}

type boundedMemoryWriter struct {
	buffer   bytes.Buffer
	maximum  int
	overflow bool
}

func (writer *boundedMemoryWriter) Write(data []byte) (int, error) {
	remaining := writer.maximum - writer.buffer.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = writer.buffer.Write(data[:remaining])
	}
	if remaining < len(data) {
		writer.overflow = true
	}
	return len(data), nil
}

func (writer *boundedMemoryWriter) Bytes() []byte {
	return append([]byte(nil), writer.buffer.Bytes()...)
}

func readWorkIntakeProposalOutput(adapter, outputPath string, stdout []byte) (*v1.WorkIntakeProposal, bool, bool) {
	if adapter == "codex" {
		raw, err := safeio.ReadRegularFile(outputPath, 32<<10, 0o077)
		if err != nil {
			return nil, false, false
		}
		proposal, valid := decodeWorkIntakeProposalCandidate(raw)
		return proposal, true, valid
	}
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, false, false
	}
	if proposal, valid := decodeWorkIntakeProposalCandidate(trimmed); valid {
		return proposal, true, true
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	for index := len(lines) - 1; index >= 0; index-- {
		if proposal, valid := extractWorkIntakeProposalCandidate(lines[index]); valid {
			return proposal, true, true
		}
	}
	return nil, true, false
}

func extractWorkIntakeProposalCandidate(raw []byte) (*v1.WorkIntakeProposal, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, false
	}
	for _, field := range []string{"structured_output", "result", "text", "content", "part"} {
		candidate, exists := envelope[field]
		if !exists {
			continue
		}
		if proposal, valid := decodeWorkIntakeProposalCandidate(candidate); valid {
			return proposal, true
		}
		var encoded string
		if json.Unmarshal(candidate, &encoded) == nil {
			if proposal, valid := decodeWorkIntakeProposalCandidate([]byte(encoded)); valid {
				return proposal, true
			}
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(candidate, &nested) == nil {
			for _, nestedField := range []string{"text", "content", "result"} {
				if value, ok := nested[nestedField]; ok {
					if proposal, valid := extractWorkIntakeProposalCandidate([]byte(`{"result":` + string(value) + `}`)); valid {
						return proposal, true
					}
				}
			}
		}
	}
	return nil, false
}

func decodeWorkIntakeProposalCandidate(raw []byte) (*v1.WorkIntakeProposal, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var proposal v1.WorkIntakeProposal
	if err := decoder.Decode(&proposal); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, false
	}
	proposal = normalizeWorkIntakeProposal(proposal)
	if validation.WorkIntakeProposal(proposal) != nil {
		return nil, false
	}
	return &proposal, true
}

func normalizeWorkIntakeProposal(value v1.WorkIntakeProposal) v1.WorkIntakeProposal {
	normalize := func(text string) string { return strings.TrimSpace(norm.NFC.String(text)) }
	result := v1.WorkIntakeProposal{SchemaVersion: value.SchemaVersion, Kind: value.Kind}
	if value.Kind == v1.WorkIntakeProposalQuestions {
		result.Questions = make([]string, len(value.Questions))
		for index, question := range value.Questions {
			result.Questions[index] = normalize(question)
		}
		return result
	}
	if value.Draft == nil {
		return result
	}
	draft := *value.Draft
	draft.Problem = normalize(draft.Problem)
	draft.AffectedUser = normalize(draft.AffectedUser)
	draft.DesiredOutcome = normalize(draft.DesiredOutcome)
	draft.Title = normalize(draft.Title)
	draft.Description = normalize(draft.Description)
	draft.AcceptanceCriteria = append([]string(nil), draft.AcceptanceCriteria...)
	for index, criterion := range draft.AcceptanceCriteria {
		draft.AcceptanceCriteria[index] = normalize(criterion)
	}
	draft.NonGoals = append([]string{}, draft.NonGoals...)
	for index, nonGoal := range draft.NonGoals {
		draft.NonGoals[index] = normalize(nonGoal)
	}
	draft.Risk = normalize(draft.Risk)
	draft.WorkType = normalize(draft.WorkType)
	result.Draft = &draft
	return result
}
