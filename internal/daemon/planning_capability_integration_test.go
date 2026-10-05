package daemon_test

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mockcp"
)

func planningGit(t *testing.T, origin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = origin
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func awaitConfiguredPlanningCapability(t *testing.T, cp *mockcp.Server, stateDir, binary string) {
	t.Helper()
	var manifest v1.CapabilityManifest
	waitFor(t, "configured planning capability", 20*time.Second, func() bool {
		for _, event := range cp.Events() {
			payload, ok := event.Payload.(map[string]any)
			if !ok || payload["kind"] != "capability_manifest" {
				continue
			}
			raw, err := json.Marshal(payload["manifest"])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), binary) {
				t.Fatal("signed capability exposes the configured executable path")
			}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			for _, agent := range manifest.Agents {
				if agent.Provider == "claude" {
					if !agent.Available || !agent.AuthReady || agent.Version != "controlled-planning-agent 1.0" {
						t.Fatalf("configured planning runtime not ready: %+v", agent)
					}
					return true
				}
			}
		}
		return false
	})
	key, err := identity.NewStore(stateDir).LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := manifest
	unsigned.Signature = ""
	raw, err := json.Marshal(&unsigned)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], signature) {
		t.Fatal("configured capability signature does not verify against enrolled identity")
	}
}

func assertPlanningResourcesRemoved(t *testing.T, receipt string) {
	t.Helper()
	raw, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(paths) != 3 {
		t.Fatalf("expected workspace, socket and token paths, got %d", len(paths))
	}
	waitFor(t, "planning workspace and MCP cleanup", 5*time.Second, func() bool {
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				t.Fatalf("resource receipt is not absolute: %q", path)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				return false
			}
		}
		return true
	})
}
