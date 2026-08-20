package previewobserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func observerProfileConfig() config.PreviewObservationProfileConfig {
	return config.PreviewObservationProfileConfig{
		BlueprintID:             "node-web-app",
		Version:                 "2026.08.20",
		Digest:                  strings.Repeat("a", 64),
		AllowedHostnameSuffixes: []string{"preview.example.com"},
		MaxResponseBytes:        64 << 10,
		HealthChecks: map[string]config.PreviewObservationHealthCheckConfig{
			"api":  {Path: "/api/health", ExpectedStatus: []int{200}, TimeoutSeconds: 5},
			"root": {Path: "/healthz", ExpectedStatus: []int{200, 204}, TimeoutSeconds: 10},
		},
	}
}

func TestRegistryWithdrawsProfileWhenLocalCredentialBecomesUnsafe(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "authorization")
	if err := os.WriteFile(credential, []byte("Bearer local-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := observerProfileConfig()
	profile.AuthorizationFile = credential
	registry, err := NewRegistry(map[string]config.PreviewObservationProfileConfig{
		"node-preview": profile,
	}, Dependencies{})
	if err != nil || len(registry.ReadyProfiles()) != 1 {
		t.Fatalf("initial profile readiness = %v, %v", registry.ReadyProfiles(), err)
	}
	if err := os.Chmod(credential, 0o640); err != nil {
		t.Fatal(err)
	}
	if profiles := registry.ReadyProfiles(); len(profiles) != 0 {
		t.Fatalf("unsafe credential remained advertised: %#v", profiles)
	}
	if _, ok := registry.profileFor(targetOperation("https://branch.preview.example.com")); ok {
		t.Fatal("unsafe credential remained executable")
	}
}

func TestRegistryAdvertisesOnlySafeSortedProfileIdentity(t *testing.T) {
	registry, err := NewRegistry(map[string]config.PreviewObservationProfileConfig{
		"node-preview": observerProfileConfig(),
	}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	profiles := registry.ReadyProfiles()
	if len(profiles) != 1 {
		t.Fatalf("profiles = %#v", profiles)
	}
	want := v1.PreviewObservationProfileCapability{
		BlueprintID: "node-web-app",
		Version:     "2026.08.20",
		Digest:      strings.Repeat("a", 64),
		HealthCheckIDs: []string{
			"api", "root",
		},
	}
	if profiles[0].BlueprintID != want.BlueprintID || profiles[0].Version != want.Version ||
		profiles[0].Digest != want.Digest || strings.Join(profiles[0].HealthCheckIDs, ",") != "api,root" {
		t.Fatalf("profile = %#v, want %#v", profiles[0], want)
	}
	profiles[0].HealthCheckIDs[0] = "changed"
	if registry.ReadyProfiles()[0].HealthCheckIDs[0] != "api" {
		t.Fatal("caller mutated registry capability")
	}
}

func TestRegistryRejectsAmbiguousCapabilityIdentity(t *testing.T) {
	duplicate := observerProfileConfig()
	_, err := NewRegistry(map[string]config.PreviewObservationProfileConfig{
		"first":  observerProfileConfig(),
		"second": duplicate,
	}, Dependencies{})
	if err == nil {
		t.Fatal("ambiguous preview observation capability accepted")
	}
}

func TestRegistryRequiresExactBlueprintAndCompleteCheckInventory(t *testing.T) {
	registry, err := NewRegistry(map[string]config.PreviewObservationProfileConfig{
		"node-preview": observerProfileConfig(),
	}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	operation := v1.PreviewObservationOperation{
		Blueprint: v1.PreviewObservationBlueprint{
			BlueprintID: "node-web-app", Version: "2026.08.20", Digest: strings.Repeat("a", 64),
		},
		RequiredHealthCheckIDs: []string{"api", "root"},
	}
	if _, ok := registry.profileFor(operation); !ok {
		t.Fatal("exact profile was not found")
	}
	operation.RequiredHealthCheckIDs = []string{"api"}
	if _, ok := registry.profileFor(operation); ok {
		t.Fatal("partial health-check inventory matched")
	}
	operation.RequiredHealthCheckIDs = []string{"api", "root"}
	operation.Blueprint.Digest = strings.Repeat("b", 64)
	if _, ok := registry.profileFor(operation); ok {
		t.Fatal("foreign blueprint digest matched")
	}
}
