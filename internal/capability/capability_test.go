package capability

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
)

func fakeDetector() *Detector {
	d := New(
		"r-1",
		"0.1.0",
		map[string]string{"pool": "test"},
		map[string]string{"trust-z": "unused", "trust-a": "unused"},
	)
	d.LookPath = func(name string) (string, error) {
		if name == "git" || name == "codex" || name == "opencode" || name == "tmux" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	d.RunVersion = func(ctx context.Context, name string, args ...string) (string, error) {
		return name + " version 1.0", nil
	}
	return d
}

func TestCollect(t *testing.T) {
	m := fakeDetector().Collect(context.Background())
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		t.Fatalf("os/arch mismatch")
	}
	if m.GitVersion != "git version 1.0" {
		t.Fatalf("GitVersion = %q", m.GitVersion)
	}
	if m.ContainerEngine != "" {
		t.Fatalf("ContainerEngine = %q, want empty", m.ContainerEngine)
	}
	var codex *v1.AgentCapability
	for i := range m.Agents {
		if m.Agents[i].Provider == "codex" {
			codex = &m.Agents[i]
		}
	}
	if codex == nil || !codex.Available || codex.Version != "codex version 1.0" {
		t.Fatalf("codex capability = %+v", codex)
	}
	if !codex.ManagedHooksSupported || codex.ManagedHooksVersion != "codex-command-hooks/v1" || len(codex.ManagedHookEvents) == 0 {
		t.Fatalf("codex managed hook capability = %+v", codex)
	}
	var openCode *v1.AgentCapability
	for i := range m.Agents {
		if m.Agents[i].Provider == "opencode" {
			openCode = &m.Agents[i]
		}
	}
	if openCode == nil || !openCode.Available || openCode.Version != "opencode version 1.0" {
		t.Fatalf("OpenCode capability = %+v", openCode)
	}
	if openCode.ManagedHooksSupported || openCode.ManagedHooksVersion != "" || len(openCode.ManagedHookEvents) != 0 {
		t.Fatalf("OpenCode must not claim managed hook events: %+v", openCode)
	}
	if len(m.Supervisors) != 1 || m.Supervisors[0] != "tmux" {
		t.Fatalf("Supervisors = %v", m.Supervisors)
	}
	if m.Labels["pool"] != "test" {
		t.Fatalf("labels not propagated")
	}
	if m.AgentPackDeliveryAuthorityVersion != v1.AgentPackDeliveryAuthorityVersion {
		t.Fatalf("agent pack authority version = %d", m.AgentPackDeliveryAuthorityVersion)
	}
	if len(m.TrustedAgentPackKeyIDs) != 2 || m.TrustedAgentPackKeyIDs[0] != "trust-a" || m.TrustedAgentPackKeyIDs[1] != "trust-z" {
		t.Fatalf("trusted Agent Pack key IDs = %v", m.TrustedAgentPackKeyIDs)
	}
	if m.SourcePublicationTransportVersion != 0 || len(m.SourcePublicationProviders) != 0 {
		t.Fatalf("source publication must remain unadvertised until the runtime adapter exists: %+v", m)
	}
}

func TestCollectReportsRootless(t *testing.T) {
	d := fakeDetector()
	d.LookPath = func(name string) (string, error) {
		if name == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("not found")
	}
	d.ProbeDocker = func(ctx context.Context) isolation.DockerInfo {
		return isolation.DockerInfo{Available: true, Rootless: true, ServerVersion: "26.1.0"}
	}
	m := d.Collect(context.Background())
	if m.ContainerEngine != "docker" || !m.ContainerRootless {
		t.Fatalf("engine = %q rootless = %v", m.ContainerEngine, m.ContainerRootless)
	}

	d.ProbeDocker = func(ctx context.Context) isolation.DockerInfo {
		return isolation.DockerInfo{Available: true, ServerVersion: "26.1.0"}
	}
	m = d.Collect(context.Background())
	if m.ContainerRootless {
		t.Fatal("rootful docker must not report rootless")
	}
}

func TestCollectAdvertisesOnlyConfiguredSourcePublicationProviders(t *testing.T) {
	detector := fakeDetector()
	detector.SourcePublicationProviders = []string{"gitlab", "github", "github"}
	manifest := detector.Collect(context.Background())
	if manifest.SourcePublicationTransportVersion != v1.SourcePublicationTransportVersion {
		t.Fatalf("transport version = %d", manifest.SourcePublicationTransportVersion)
	}
	if len(manifest.SourcePublicationProviders) != 2 || manifest.SourcePublicationProviders[0] != "github" ||
		manifest.SourcePublicationProviders[1] != "gitlab" {
		t.Fatalf("providers = %v", manifest.SourcePublicationProviders)
	}

	detector.SourcePublicationProviders[0] = "changed"
	if manifest.SourcePublicationProviders[0] != "github" {
		t.Fatal("manifest providers alias detector configuration")
	}
}

func TestCollectAdvertisesOnlyUsablePreviewObservationProfiles(t *testing.T) {
	detector := fakeDetector()
	detector.PreviewObservationProfiles = []v1.PreviewObservationProfileCapability{
		{
			BlueprintID: "node-web-app", Version: "2026.08.20", Digest: strings.Repeat("a", 64),
			HealthCheckIDs: []string{"api", "root"},
		},
	}
	manifest := detector.Collect(context.Background())
	if manifest.PreviewObservationTransportVersion != v1.PreviewObservationTransportVersion ||
		len(manifest.PreviewObservationProfiles) != 1 ||
		strings.Join(manifest.PreviewObservationProfiles[0].HealthCheckIDs, ",") != "api,root" {
		t.Fatalf("preview observation capability = %#v", manifest.PreviewObservationProfiles)
	}
	detector.PreviewObservationProfiles[0].HealthCheckIDs[0] = "changed"
	if manifest.PreviewObservationProfiles[0].HealthCheckIDs[0] != "api" {
		t.Fatal("capability manifest shares mutable preview profile state")
	}
}

func TestCollectAdvertisesExactReviewerIdentityInstallations(t *testing.T) {
	detector := fakeDetector()
	detector.ReviewerIdentityVerificationInstallations = []v1.ReviewerIdentityVerificationInstallationCapability{
		{InstallationID: "installation-b", Provider: "gitlab", ProviderInstanceKey: "gitlab.example"},
		{InstallationID: "installation-a", Provider: "github", ProviderInstanceKey: "github.com"},
	}
	manifest := detector.Collect(context.Background())
	if manifest.ReviewerIdentityVerificationTransportVersion != v1.ReviewerIdentityVerificationTransportVersion ||
		len(manifest.ReviewerIdentityVerificationInstallations) != 2 ||
		manifest.ReviewerIdentityVerificationInstallations[0].InstallationID != "installation-a" ||
		strings.Join(manifest.ReviewerIdentityVerificationProviders, ",") != "github,gitlab" {
		t.Fatalf("reviewer identity capability = %+v", manifest)
	}
	detector.ReviewerIdentityVerificationInstallations[0].InstallationID = "changed"
	if manifest.ReviewerIdentityVerificationInstallations[1].InstallationID != "installation-b" {
		t.Fatal("manifest shares reviewer identity installation state")
	}
}

func TestCollectAdvertisesExactProviderReviewerAssignmentInstallations(t *testing.T) {
	detector := fakeDetector()
	detector.ProviderReviewerAssignmentInstallations = []v1.ProviderReviewerAssignmentInstallationCapability{
		{InstallationID: "installation-b", Provider: "gitlab", ProviderInstanceKey: "gitlab.example"},
		{InstallationID: "installation-a", Provider: "github", ProviderInstanceKey: "github.com"},
	}
	manifest := detector.Collect(context.Background())
	if manifest.ProviderReviewerAssignmentTransportVersion != v1.ProviderReviewerAssignmentTransportVersion ||
		len(manifest.ProviderReviewerAssignmentInstallations) != 2 ||
		manifest.ProviderReviewerAssignmentInstallations[0].InstallationID != "installation-a" ||
		strings.Join(manifest.ProviderReviewerAssignmentProviders, ",") != "github,gitlab" {
		t.Fatalf("provider reviewer assignment capability = %+v", manifest)
	}
	detector.ProviderReviewerAssignmentInstallations[0].InstallationID = "changed"
	if manifest.ProviderReviewerAssignmentInstallations[1].InstallationID != "installation-b" {
		t.Fatal("manifest shares provider reviewer assignment installation state")
	}
}

func TestSignVerify(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	d := fakeDetector()
	m := d.Collect(context.Background())
	if err := d.Sign(&m, key); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if m.Signature == "" {
		t.Fatal("empty signature")
	}
	sig, _ := base64.StdEncoding.DecodeString(m.Signature)
	m.Signature = ""
	payload, _ := json.Marshal(&m)
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], sig) {
		t.Fatal("signature does not verify")
	}
}
