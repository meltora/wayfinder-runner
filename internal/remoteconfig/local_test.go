package remoteconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndLoadLocalFilesArePrivateAndStrict(t *testing.T) {
	local := testLocal(t)
	local.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": "/home/runner/.claude"}}
	local.AcceptedPackKeyFingerprints = []string{strings.Repeat("a", 64)}
	local.AcknowledgeDirect = true
	remote := []byte(`{"cached":true}`)
	if err := SaveFiles(local.StateDir, local, remote); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{LocalFileName, RemoteFileName} {
		info, err := os.Stat(filepath.Join(local.StateDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v", name, info.Mode().Perm())
		}
	}
	loaded, err := LoadLocal(local.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ControlPlaneURL != local.ControlPlaneURL || loaded.StateDir != local.StateDir || !loaded.AcknowledgeDirect ||
		loaded.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/home/runner/.claude" || loaded.AcceptedPackKeyFingerprints[0] != strings.Repeat("a", 64) {
		t.Fatalf("loaded = %+v", loaded)
	}
	cached, err := LoadCachedDelivery(local.StateDir)
	if err != nil || string(cached) != string(remote) {
		t.Fatalf("cached = %q, %v", cached, err)
	}
}

func TestLoadLocalRejectsMissingUnknownForeignAndExposedFiles(t *testing.T) {
	local := testLocal(t)
	if _, err := LoadLocal(local.StateDir); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing err = %v", err)
	}
	if err := os.MkdirAll(local.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(local.StateDir, LocalFileName)
	write := func(content string, mode os.FileMode) {
		t.Helper()
		_ = os.Remove(path)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	base := "control_plane_url: " + local.ControlPlaneURL + "\nstate_dir: " + local.StateDir + "\n"
	write(base+"isolation: direct\n", 0o600)
	if _, err := LoadLocal(local.StateDir); err == nil {
		t.Fatal("accepted a remote-owned key in the local file")
	}
	write(base+"---\ncontrol_plane_url: x\n", 0o600)
	if _, err := LoadLocal(local.StateDir); err == nil {
		t.Fatal("accepted multiple YAML documents")
	}
	write("control_plane_url: "+local.ControlPlaneURL+"\nstate_dir: /elsewhere/state\n", 0o600)
	if _, err := LoadLocal(local.StateDir); err == nil {
		t.Fatal("accepted a local file for another state directory")
	}
	write(base, 0o644)
	if _, err := LoadLocal(local.StateDir); err == nil {
		t.Fatal("accepted a group-readable local file")
	}
	write(base, 0o600)
	if _, err := LoadLocal(local.StateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCachedDelivery(local.StateDir); err == nil {
		t.Fatal("missing cache loaded")
	}
}

func TestConfigurationURLFollowsTheGatewayOrigin(t *testing.T) {
	for gateway, want := range map[string]string{
		"wss://gateway.example.test/v1/runners/stream":        "https://gateway.example.test/v1/runners/configuration",
		"wss://gateway.example.test:7443/v1/runners/stream":   "https://gateway.example.test:7443/v1/runners/configuration",
		"wss://gateway.example.test":                          "https://gateway.example.test/v1/runners/configuration",
		"wss://example.test/runner-gateway/v1/runners/stream": "https://example.test/runner-gateway/v1/runners/configuration",
	} {
		got, err := ConfigurationURL(gateway)
		if err != nil || got != want {
			t.Fatalf("%s => %s, %v", gateway, got, err)
		}
	}
	for _, gateway := range []string{"", "ws://gateway.example.test", "https://gateway.example.test", "wss://u:p@gateway.example.test", "wss://gateway.example.test/x?y=1", "wss://gateway.example.test#x", "wss:///path"} {
		if _, err := ConfigurationURL(gateway); err == nil {
			t.Fatalf("accepted gateway %q", gateway)
		}
	}
}
