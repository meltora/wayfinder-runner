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
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestClaudeCapabilityAliasesReuseOneConfiguredProbe(t *testing.T) {
	for _, mode := range []string{"ready", "authentication-unavailable", "executable-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			const binary = "/private/controlled/claude"
			detector := fakeDetector().WithProviderBinaries(map[string]string{"claude": binary})
			paths, versions, auth := 0, 0, 0
			detector.LookPath = func(name string) (string, error) {
				if name == "claude" || name == "claude-code" {
					t.Fatal("alias/default executable must never be probed")
				}
				if name == binary {
					paths++
					if mode != "executable-unavailable" {
						return binary, nil
					}
				}
				return "", errors.New("missing")
			}
			detector.RunVersion = func(_ context.Context, name string, args ...string) (string, error) {
				versions++
				if name != binary || !reflect.DeepEqual(args, []string{"--version"}) {
					t.Fatal("unexpected version probe")
				}
				return "controlled version", nil
			}
			detector.AuthReady = func(name string) bool {
				auth++
				if name != "claude" {
					t.Fatal("alias auth probe")
				}
				return mode == "ready"
			}
			manifest := detector.Collect(context.Background())
			legacy := providerCapability(t, manifest, "claude")
			canonical := providerCapability(t, manifest, "claude-code")
			canonical.Provider = "claude"
			if !reflect.DeepEqual(legacy, canonical) {
				t.Fatalf("alias changed probed facts: %+v / %+v", legacy, canonical)
			}
			available := mode != "executable-unavailable"
			if legacy.Available != available || legacy.AuthReady != (mode == "ready") {
				t.Fatalf("unexpected facts: %+v", legacy)
			}
			expected := 0
			if available {
				expected = 1
			}
			if paths != 1 || versions != expected || auth != expected {
				t.Fatalf("probe counts path=%d version=%d auth=%d", paths, versions, auth)
			}
			var names []string
			for _, agent := range manifest.Agents {
				names = append(names, agent.Provider)
			}
			if !reflect.DeepEqual(names, []string{"codex", "claude", "claude-code", "opencode"}) {
				t.Fatalf("unstable aliases: %v", names)
			}
		})
	}
}

func TestProviderCapabilityAliasesCloneHooksWithoutInventingAliases(t *testing.T) {
	original := v1.AgentCapability{Provider: "claude", ManagedHookEvents: []string{"SessionStart"}}
	agents := providerCapabilityAliases(original)
	if len(agents) != 2 || agents[0].Provider != "claude" || agents[1].Provider != "claude-code" {
		t.Fatal("missing fixed compatibility alias")
	}
	agents[1].ManagedHookEvents[0] = "changed alias"
	if original.ManagedHookEvents[0] != "SessionStart" || agents[0].ManagedHookEvents[0] != "SessionStart" {
		t.Fatal("alias hooks share mutable state")
	}
	agents[0].ManagedHookEvents[0] = "changed legacy"
	if original.ManagedHookEvents[0] != "SessionStart" {
		t.Fatal("legacy entry mutates input")
	}
	for _, name := range []string{"codex", "opencode", "claude-code", "custom"} {
		result := providerCapabilityAliases(v1.AgentCapability{Provider: name})
		if len(result) != 1 || result[0].Provider != name {
			t.Fatalf("invented alias for %s", name)
		}
	}
}

func TestCapabilitySignatureBindsLegacyAndCanonicalClaudeFacts(t *testing.T) {
	detector := fakeDetector()
	manifest := detector.Collect(context.Background())
	providerCapability(t, manifest, "claude")
	providerCapability(t, manifest, "claude-code")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = detector.Sign(&manifest, key); err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(candidate v1.CapabilityManifest) bool {
		candidate.Signature = ""
		payload, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		return ecdsa.VerifyASN1(&key.PublicKey, digest[:], signature)
	}
	if !verify(manifest) {
		t.Fatal("complete alias manifest signature invalid")
	}
	for _, name := range []string{"claude", "claude-code"} {
		changed := manifest
		changed.Agents = append([]v1.AgentCapability(nil), manifest.Agents...)
		for i, agent := range changed.Agents {
			if agent.Provider == name {
				changed.Agents[i].Available = !agent.Available
			}
		}
		if verify(changed) {
			t.Fatalf("signature did not bind %s", name)
		}
	}
}
