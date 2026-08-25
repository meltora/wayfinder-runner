package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/provider"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/supervisor"
)

const workIntakeProposalExecutionConfigDomain = "work_intake_proposal_model_execution_config.v1"

func (generator *providerWorkIntakeProposalGenerator) ModelExecutionConfigDigest(ctx context.Context) (string, error) {
	if generator.probeDocker == nil {
		return "", fmt.Errorf("Docker isolation attestation is unavailable")
	}
	dockerInfo := generator.probeDocker(ctx)
	if !dockerInfo.Available || (!dockerInfo.Rootless && !dockerInfo.UsernsRemap) {
		return "", fmt.Errorf("hardened Docker isolation is unavailable")
	}
	if generator.attestNetwork == nil || generator.networkAttestation.ID == "" {
		return "", fmt.Errorf("restricted Docker network attestation is unavailable")
	}
	if err := generator.attestNetwork(ctx, generator.networkAttestation); err != nil {
		return "", err
	}
	if generator.attestEffectivePolicy == nil || generator.effectivePolicy.NetworkID == "" {
		return "", fmt.Errorf("effective restricted egress policy attestation is unavailable")
	}
	if err := generator.attestEffectivePolicy(ctx, generator.effectivePolicy); err != nil {
		return "", err
	}
	root, err := generator.preparePrivateExecutionRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(root) }()
	workspace := filepath.Join(root, "workspace")
	privateConfig := filepath.Join(root, "config")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(privateConfig, 0o700); err != nil {
		return "", err
	}
	if err := safeio.AtomicWriteFile(privateConfig, "schema.json", []byte(workIntakeProposalOutputSchema), 0o600); err != nil {
		return "", err
	}
	adapter, err := provider.For(generator.cfg.WorkIntakeProposal.Adapter)
	if err != nil {
		return "", err
	}
	builder, ok := adapter.(promptOnlySpecBuilder)
	if !ok {
		return "", fmt.Errorf("adapter does not support prompt-only execution")
	}
	spec, err := builder.BuildPromptOnlySpec(provider.PromptOnlyParams{
		ExecutionID: "execution-config", WorkDir: workspace, ConfigDir: privateConfig,
		Prompt: "untrusted-input-on-stdin", SchemaPath: filepath.Join(privateConfig, "schema.json"),
		OutputPath: filepath.Join(privateConfig, "result.json"),
		BinaryPath: generator.cfg.ProviderBinaries[generator.cfg.WorkIntakeProposal.Adapter],
		Model:      generator.cfg.WorkIntakeProposal.Model,
	})
	if err != nil {
		return "", err
	}
	return generator.modelExecutionConfigDigestForSpec(ctx, root, spec)
}

func (generator *providerWorkIntakeProposalGenerator) modelExecutionConfigDigestForSpec(
	ctx context.Context,
	executionRoot string,
	spec supervisor.Spec,
) (string, error) {
	if generator.probeDocker == nil {
		return "", fmt.Errorf("Docker isolation attestation is unavailable")
	}
	dockerInfo := generator.probeDocker(ctx)
	if !dockerInfo.Available || (!dockerInfo.Rootless && !dockerInfo.UsernsRemap) {
		return "", fmt.Errorf("hardened Docker isolation is unavailable")
	}
	versionRunner := generator.runVersion
	if versionRunner == nil {
		versionRunner = func(ctx context.Context, binary string) (string, error) {
			return runContainerProviderVersion(ctx, generator.cfg.JobImage, binary)
		}
	}
	version, err := versionRunner(ctx, spec.Command)
	if err != nil {
		return "", fmt.Errorf("read consultation adapter version: %w", err)
	}
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 4096 || strings.ContainsRune(version, '\x00') {
		return "", fmt.Errorf("consultation adapter returned an invalid version")
	}
	args := make([]string, len(spec.Args))
	for index, argument := range spec.Args {
		args[index] = strings.ReplaceAll(argument, executionRoot, "$EXECUTION_ROOT")
	}
	environment := provider.ContainerEnvironment(spec.Env)
	sort.Strings(environment)
	policy := generator.workIntakeProposalIsolationPolicy(
		executionRoot, filepath.Join(executionRoot, "home"), "execution-config",
		generator.cfg.WorkIntakeProposal.Adapter, environment,
	)
	dockerCommand, dockerArgs, err := (isolation.Docker{}).Wrap(spec.Command, spec.Args, policy)
	if err != nil {
		return "", err
	}
	for index, argument := range dockerArgs {
		dockerArgs[index] = strings.ReplaceAll(argument, executionRoot, "$EXECUTION_ROOT")
	}
	schemaDigest := sha256.Sum256([]byte(workIntakeProposalOutputSchema))
	emptyMCPDigest := ""
	if generator.cfg.WorkIntakeProposal.Adapter == "claude" {
		digest := sha256.Sum256([]byte(`{"mcpServers":{}}`))
		emptyMCPDigest = hex.EncodeToString(digest[:])
	}
	descriptor := struct {
		DigestDomain          string                             `json:"digest_domain"`
		Adapter               string                             `json:"adapter"`
		AdapterVersion        string                             `json:"adapter_version"`
		Binary                string                             `json:"binary"`
		Model                 string                             `json:"model"`
		ProviderArgs          []string                           `json:"provider_args"`
		ProviderEnvironment   []string                           `json:"provider_environment"`
		StdinMode             string                             `json:"stdin_mode"`
		DockerImage           string                             `json:"docker_image"`
		DockerServerVersion   string                             `json:"docker_server_version"`
		DockerRootless        bool                               `json:"docker_rootless"`
		DockerUsernsRemap     bool                               `json:"docker_userns_remap"`
		DockerNetwork         isolation.DockerNetworkAttestation `json:"docker_network"`
		EffectiveEgressPolicy egresspolicy.Binding               `json:"effective_egress_policy"`
		DockerMemoryMB        int                                `json:"docker_memory_mb"`
		DockerCPUs            float64                            `json:"docker_cpus"`
		DockerPIDs            int                                `json:"docker_pids"`
		DockerReadOnly        bool                               `json:"docker_read_only"`
		DockerNestedSandbox   bool                               `json:"docker_nested_sandbox"`
		DockerLogDriver       string                             `json:"docker_log_driver"`
		PolicyLeaseDurationMS int64                              `json:"policy_lease_duration_ms"`
		DockerWrapperPolicy   string                             `json:"docker_wrapper_policy"`
		DockerCommand         string                             `json:"docker_command"`
		DockerArgs            []string                           `json:"docker_args"`
		OutputSchemaDigest    string                             `json:"output_schema_digest"`
		StrictEmptyMCPDigest  string                             `json:"strict_empty_mcp_digest"`
	}{
		DigestDomain: workIntakeProposalExecutionConfigDomain,
		Adapter:      generator.cfg.WorkIntakeProposal.Adapter, AdapterVersion: version,
		Binary: spec.Command, Model: generator.cfg.WorkIntakeProposal.Model,
		ProviderArgs: args, ProviderEnvironment: environment, StdinMode: "pipe",
		DockerImage: generator.cfg.JobImage, DockerServerVersion: dockerInfo.ServerVersion,
		DockerRootless: dockerInfo.Rootless, DockerUsernsRemap: dockerInfo.UsernsRemap,
		DockerNetwork: generator.networkAttestation, EffectiveEgressPolicy: generator.effectivePolicy,
		DockerMemoryMB: generator.cfg.JobMemoryMB, DockerCPUs: generator.cfg.JobCPUs,
		DockerPIDs: generator.cfg.JobPIDs, DockerReadOnly: true,
		DockerNestedSandbox: generator.cfg.WorkIntakeProposal.Adapter != "opencode",
		DockerLogDriver:     "none", DockerWrapperPolicy: "work-intake-proposal-docker/v1",
		PolicyLeaseDurationMS: workIntakeProposalPolicyLeaseDuration.Milliseconds(),
		DockerCommand:         dockerCommand, DockerArgs: dockerArgs,
		OutputSchemaDigest: hex.EncodeToString(schemaDigest[:]), StrictEmptyMCPDigest: emptyMCPDigest,
	}
	material, err := json.Marshal(descriptor)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(material)
	return hex.EncodeToString(digest[:]), nil
}

func (generator *providerWorkIntakeProposalGenerator) workIntakeProposalIsolationPolicy(
	executionRoot, jobHome, attemptID, adapter string,
	environment []string,
) isolation.Policy {
	return isolation.Policy{
		MemoryMB: generator.cfg.JobMemoryMB, CPUs: generator.cfg.JobCPUs, PIDs: generator.cfg.JobPIDs,
		NetworkName: generator.networkAttestation.ID,
		ReadOnlyFS:  true, WorkDir: filepath.Join(executionRoot, "workspace"), WorkspaceRoot: executionRoot,
		JobHome: jobHome, Image: generator.cfg.JobImage, RunnerID: generator.runnerID,
		AttemptID: attemptID, NestedSandbox: adapter != "opencode", DisableLogs: true,
		Environment: append([]string(nil), environment...),
	}
}

func runContainerProviderVersion(ctx context.Context, image, binary string) (string, error) {
	if image == "" || binary == "" {
		return "", fmt.Errorf("container image and provider binary are required")
	}
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// #nosec G204 -- image is configuration-validated as an immutable sha256
	// reference and binary is administrator-configured from the provider allowlist.
	command := exec.CommandContext(versionCtx, "docker", "run", "--rm", "--network", "none",
		"--log-driver", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", image, binary, "--version")
	command.Env = isolation.DockerEnvironment()
	output, err := command.CombinedOutput()
	return string(output), err
}
