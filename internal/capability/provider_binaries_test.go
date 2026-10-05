package capability

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
)

func providerCapability(t *testing.T, manifest v1.CapabilityManifest, name string) v1.AgentCapability {
	t.Helper()
	for _, agent := range manifest.Agents {
		if agent.Provider == name {
			return agent
		}
	}
	t.Fatalf("missing provider %q", name)
	return v1.AgentCapability{}
}

func TestConfiguredProviderBinaryIsUsedForPresenceAndVersion(t *testing.T) {
	for _, name := range []string{"codex", "claude", "opencode"} {
		t.Run(name, func(t *testing.T) {
			path := "/private/operator tools/" + name
			detector := fakeDetector().WithProviderBinaries(map[string]string{name: path})
			var lookedUp, versioned, authenticated []string
			detector.LookPath = func(binary string) (string, error) {
				lookedUp = append(lookedUp, binary)
				if binary == path {
					return path, nil
				}
				return "", errors.New("not installed")
			}
			detector.RunVersion = func(_ context.Context, binary string, args ...string) (string, error) {
				versioned = append(versioned, binary)
				if !reflect.DeepEqual(args, []string{"--version"}) {
					t.Fatalf("version arguments: %v", args)
				}
				return "controlled version", nil
			}
			detector.AuthReady = func(adapter string) bool { authenticated = append(authenticated, adapter); return false }
			manifest := detector.Collect(context.Background())
			agent := providerCapability(t, manifest, name)
			if !agent.Available || agent.Version != "controlled version" || agent.AuthReady {
				t.Fatalf("configured capability: %+v", agent)
			}
			if !reflect.DeepEqual(versioned, []string{path}) || !reflect.DeepEqual(authenticated, []string{name}) {
				t.Fatalf("probes: %v auth: %v", versioned, authenticated)
			}
			for _, binary := range lookedUp {
				if binary == name {
					t.Fatal("probed default executable despite configured override")
				}
			}
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), path) {
				t.Fatal("private configured path leaked into manifest")
			}
		})
	}
}

func TestUnavailableConfiguredProviderNeverFallsBackToPATH(t *testing.T) {
	for _, name := range []string{"codex", "claude", "opencode"} {
		t.Run(name, func(t *testing.T) {
			path := "/missing/" + name
			detector := fakeDetector().WithProviderBinaries(map[string]string{name: path})
			detector.LookPath = func(binary string) (string, error) {
				if binary == name {
					t.Fatal("fell back to default provider")
				}
				return "", errors.New("unavailable")
			}
			detector.RunVersion = func(context.Context, string, ...string) (string, error) {
				t.Fatal("version probe for unavailable executable")
				return "", nil
			}
			detector.AuthReady = func(string) bool { t.Fatal("auth probe for unavailable executable"); return true }
			agent := providerCapability(t, detector.Collect(context.Background()), name)
			if agent.Available || agent.Version != "" || agent.AuthReady {
				t.Fatalf("unavailable configured capability: %+v", agent)
			}
		})
	}
}

func TestProviderBinaryConfigurationCopiesOnlyAllowlistedOverrides(t *testing.T) {
	source := map[string]string{"codex": "/approved/codex", "claude": "", "custom": "/private/custom"}
	original := fakeDetector()
	detector := original.WithProviderBinaries(source)
	source["codex"] = "/changed/codex"
	if detector == original || original.providerBinary("codex") != "codex" {
		t.Fatal("configuration mutated original detector")
	}
	for name, expected := range map[string]string{"codex": "/approved/codex", "claude": "claude", "opencode": "opencode", "custom": "", "": ""} {
		if got := detector.providerBinary(name); got != expected {
			t.Fatalf("binary for %q = %q", name, got)
		}
	}
	if got := detector.WithProviderBinaries(nil).providerBinary("codex"); got != "codex" {
		t.Fatalf("unset override = %q", got)
	}
}

func TestConfiguredProviderVersionFailureDoesNotChangePresenceOrAuthentication(t *testing.T) {
	detector := fakeDetector().WithProviderBinaries(map[string]string{"codex": "/approved/codex"})
	detector.LookPath = func(binary string) (string, error) {
		if binary == "/approved/codex" {
			return binary, nil
		}
		return "", errors.New("absent")
	}
	detector.RunVersion = func(context.Context, string, ...string) (string, error) { return "", errors.New("version unavailable") }
	detector.AuthReady = func(name string) bool { return name == "codex" }
	agent := providerCapability(t, detector.Collect(context.Background()), "codex")
	if !agent.Available || agent.Version != "" || !agent.AuthReady {
		t.Fatalf("presence/auth changed: %+v", agent)
	}
}

func TestConsultationCapabilityUsesConfiguredProviderWithoutFallback(t *testing.T) {
	for _, mode := range []string{"configured", "missing", "default", "unsupported", "auth-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			name, binary := "codex", "/approved/codex"
			if mode == "default" {
				binary = ""
			}
			if mode == "unsupported" {
				name = "custom"
			}
			detector := fakeDetector().WithProviderBinaries(map[string]string{name: binary})
			detector.LookPath = func(path string) (string, error) {
				if path == "docker" {
					return path, nil
				}
				if path == "custom" || path == "" {
					t.Fatalf("probed unsupported adapter: %q", path)
				}
				if mode == "missing" && (path == binary || path == "codex") {
					if path == "codex" {
						t.Fatal("consultation fell back to PATH")
					}
					return "", errors.New("missing")
				}
				if (mode == "default" && path == "codex") || path == "/approved/codex" {
					return path, nil
				}
				return "", errors.New("not installed")
			}
			detector.ProbeDocker = func(context.Context) isolation.DockerInfo {
				return isolation.DockerInfo{Available: true, Rootless: true}
			}
			detector.AuthReady = func(adapter string) bool { return adapter == "codex" && mode != "auth-unavailable" }
			detector.WorkIntakeProposalAdapter, detector.WorkIntakeProposalModel = name, "model"
			detector.WorkIntakeProposalPromptTemplateDigest = strings.Repeat("a", 64)
			detector.WorkIntakeProposalPolicyDigest = strings.Repeat("b", 64)
			detector.WorkIntakeProposalModelExecutionConfigDigest = strings.Repeat("c", 64)
			detector.WorkIntakeProposalHardened, detector.WorkIntakeProposalRestrictedEgress = true, true
			manifest := detector.Collect(context.Background())
			wanted := mode == "configured" || mode == "default"
			if (manifest.WorkIntakeProposalTransportVersion == v1.WorkIntakeProposalTransportVersion) != wanted {
				t.Fatalf("consultation mode %s published=%d", mode, manifest.WorkIntakeProposalTransportVersion)
			}
		})
	}
}

func TestProviderVersionOmitsPrivateExecutablePaths(t *testing.T) {
	for _, tc := range []struct {
		name, configured, resolved, output, expected string
	}{
		{"configured path", "/private/operator/codex", "/resolved/bin/codex", "codex 1.0 (/private/operator/codex)", ""},
		{"resolved configured path", "/private/operator/codex", "/resolved/bin/codex", "codex 1.0 (/resolved/bin/codex)", ""},
		{"resolved default path", "", "/private/bin/codex", "codex 1.0 (/private/bin/codex)", ""},
		{"ordinary version", "", "/private/bin/codex", "codex 1.0", "codex 1.0"},
		{"unresolved default name", "", "codex", "codex 1.0", "codex 1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detector := fakeDetector().WithProviderBinaries(map[string]string{"codex": tc.configured})
			detector.LookPath = func(binary string) (string, error) {
				if binary == detector.providerBinary("codex") {
					return tc.resolved, nil
				}
				return "", errors.New("not installed")
			}
			detector.RunVersion = func(context.Context, string, ...string) (string, error) { return tc.output, nil }
			detector.AuthReady = func(name string) bool { return name == "codex" }
			agent := providerCapability(t, detector.Collect(context.Background()), "codex")
			if !agent.Available || !agent.AuthReady || agent.Version != tc.expected {
				t.Fatalf("version privacy changed capability unexpectedly: %+v", agent)
			}
		})
	}
}
