// Test-only generator: outputs public verification material, never the ephemeral private key.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/capability"
	"os"
	"strings"
	"time"
)

func main() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("generate fixture key")
	}
	when, err := time.Parse(time.RFC3339Nano, "2026-09-13T12:30:45.123999999Z")
	if err != nil {
		panic("fixture time")
	}
	maxExtension := uint64(3600000)
	digest := strings.Repeat("a", 64)
	manifest := v1.CapabilityManifest{
		AgentConfigScanTransportVersion: "agent-config-scan-v1", AgentConfigScanRepositoryIDs: []string{"repo-a", "repo-b"},
		RunnerID: "runner-go-signature", CollectedAt: when, OS: "linux", Arch: "amd64", CPUs: 8, MemoryBytes: 9007199254740991, DiskFreeBytes: 4096000000,
		GitVersion: "git <2.50>&é", ContainerEngine: "docker", ContainerRootless: true, KVM: false,
		Agents: []v1.AgentCapability{
			{Provider: "codex", Version: "1.0.0", AuthReady: true, Available: true, ManagedHooksSupported: true, ManagedHooksVersion: "hooks-v1", ManagedHookEvents: []string{"before_tool", "after_tool"}},
			{Provider: "claude", AuthReady: false, Available: false, ManagedHooksSupported: false},
		},
		Supervisors:   []string{"herdr", "tmux"},
		Labels:        map[string]string{"z": "last", "2": "two", "10": "ten", "a": "<>&\u2028\u2029é", "é": "accented", "𐀀": "non-BMP", "\ue000": "private-BMP"},
		RunnerVersion: "1.0.0", ProtocolVersion: 1, AgentPackDeliveryAuthorityVersion: 1, TrustedAgentPackKeyIDs: []string{"key-a", "key-b"},
		SourcePublicationTransportVersion: 1, SourcePublicationProviders: []string{"github", "gitlab"},
		PreviewObservationTransportVersion: 1, PreviewObservationProfiles: []v1.PreviewObservationProfileCapability{{BlueprintID: "blueprint-a", Version: "v1", Digest: digest, HealthCheckIDs: []string{"health-a", "health-b"}}},
		PreviewLifecycleTransportVersion: 1, PreviewLifecycleProfiles: []v1.PreviewLifecycleProfileCapability{{BlueprintID: "blueprint-a", Version: "v1", Digest: digest, ControllerProtocolVersion: 1, Actions: []v1.PreviewLifecycleAction{v1.PreviewLifecycleDestroy, v1.PreviewLifecycleExtend}, MaxExtensionMS: &maxExtension}},
		ReviewerIdentityVerificationTransportVersion: 1, ReviewerIdentityVerificationProviders: []string{"github"}, ReviewerIdentityVerificationInstallations: []v1.ReviewerIdentityVerificationInstallationCapability{{InstallationID: "installation-a", Provider: "github", ProviderInstanceKey: "github.com"}},
		ProviderReviewerAssignmentTransportVersion: 1, ProviderReviewerAssignmentProviders: []string{"github"}, ProviderReviewerAssignmentInstallations: []v1.ProviderReviewerAssignmentInstallationCapability{{InstallationID: "installation-a", Provider: "github", ProviderInstanceKey: "github.com"}},
		ProviderReviewerAssignmentReconciliationTransportVersion: 1, ProviderReviewerAssignmentReconciliationProviders: []string{"github"}, ProviderReviewerAssignmentReconciliationInstallations: []v1.ProviderReviewerAssignmentReconciliationInstallationCapability{{InstallationID: "installation-a", Provider: "github", ProviderInstanceKey: "github.com"}},
		ProviderReviewObservationTransportVersion: 2, ProviderReviewObservationProviders: []string{"github"}, ProviderReviewObservationInstallations: []v1.ProviderReviewObservationInstallationCapability{{InstallationID: "installation-a", Provider: "github", ProviderInstanceKey: "github.com"}},
		ProviderReviewFeedbackContentTransportVersion: 1, ProviderReviewFeedbackContentProviders: []string{"github"}, ProviderReviewFeedbackContentInstallations: []v1.ProviderReviewFeedbackContentInstallationCapability{{InstallationID: "installation-a", Provider: "github", ProviderInstanceKey: "github.com"}},
		AgentRunTransportVersion: 1, WorkIntakeProposalTransportVersion: 1, WorkIntakeProposalAdapter: "codex", WorkIntakeProposalModel: "model-<>&é", WorkIntakeProposalPromptTemplateDigest: digest, WorkIntakeProposalPolicyDigest: strings.Repeat("b", 64), WorkIntakeProposalModelExecutionConfigDigest: strings.Repeat("c", 64),
		RunnerConfigurationDigest: strings.Repeat("d", 64),
	}
	detector := capability.New(manifest.RunnerID, manifest.RunnerVersion, nil, nil)
	if detector.Sign(&manifest, key) != nil {
		panic("sign fixture")
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic("encode public key")
	}
	output := map[string]any{"publicKeySpkiPem": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki})), "manifest": manifest}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(output) != nil {
		panic("write public fixture")
	}
}
