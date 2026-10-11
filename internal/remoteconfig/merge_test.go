package remoteconfig

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func testLocal(t *testing.T) Local {
	t.Helper()
	return Local{
		ControlPlaneURL: "wss://gateway.example.test/v1/runners/stream",
		StateDir:        filepath.Join(t.TempDir(), "state"),
	}
}

func parsed(t *testing.T, body map[string]any) Delivery {
	t.Helper()
	delivery, err := ParseDelivery(encode(t, body), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func TestMergeUsesLocalOwnershipAndRemoteDocument(t *testing.T) {
	key := newTestPackKey(t, "pack-key-1")
	local := testLocal(t)
	local.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": "/home/runner/.claude-work"}}
	local.ProviderBinaries = map[string]string{"claude": "/usr/local/bin/claude"}
	merged, err := Merge(local, parsed(t, deliveryBody(dockerConfig(key), key)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := merged.Config
	if cfg.ControlPlaneURL != local.ControlPlaneURL || cfg.StateDir != local.StateDir || cfg.Isolation != "docker" || cfg.AllowUnsafeDirect ||
		cfg.Provider != "claude" || cfg.JobImage != testJobImage || cfg.JobNetwork != "none" || cfg.JobMemoryMB != 4096 || cfg.JobCPUs != 2 || cfg.JobPIDs != 512 {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/home/runner/.claude-work" || cfg.ProviderBinaries["claude"] != "/usr/local/bin/claude" {
		t.Fatal("local values were not merged")
	}
	if cfg.Labels["pool"] != "eu-linux" || cfg.Repositories["repo-2"].URL != "https://github.com/acme/web.git" || cfg.SourcePublicationInstallations["installation-1"].Provider != "github" {
		t.Fatalf("remote values were not merged: %+v", cfg)
	}
	if len(cfg.AgentPackPublicKeys) != 0 {
		t.Fatalf("a delivered pack key anchored trust: %+v", merged)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMergeNeverTrustsRemotePackKeysAndCarriesTheSignedSet(t *testing.T) {
	accepted := newTestPackKey(t, "pack-key-1")
	pending := newTestPackKey(t, "pack-key-2")
	local := testLocal(t)
	config := dockerConfig(accepted, pending)
	config["agent_pack_trusted_key_set"] = map[string]any{
		"document_base64": "eyJmb3JtYXQiOjF9", "signature_base64": strings.Repeat("A", 86) + "==", "root_key_id": strings.Repeat("b", 64),
	}
	merged, err := Merge(local, parsed(t, deliveryBody(config, accepted, pending)))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Config.AgentPackPublicKeys) != 0 {
		t.Fatalf("remote pack keys were trusted: %+v", merged)
	}
	set := merged.Config.AgentPackTrustedKeySet
	if set == nil || set.RootKeyID != strings.Repeat("b", 64) {
		t.Fatalf("merged trusted key set = %+v", set)
	}
}

func TestMergeRequiresLocalDirectAcknowledgement(t *testing.T) {
	local := testLocal(t)
	if _, err := Merge(local, parsed(t, deliveryBody(directConfig()))); !errors.Is(err, ErrDirectNotAcknowledged) {
		t.Fatalf("err = %v", err)
	}
	local.AcknowledgeDirect = true
	merged, err := Merge(local, parsed(t, deliveryBody(directConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Config.Isolation != "direct" || !merged.Config.AllowUnsafeDirect || merged.Config.Provider != "codex" {
		t.Fatalf("config = %+v", merged.Config)
	}
	docker, err := Merge(local, parsed(t, deliveryBody(dockerConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if docker.Config.Isolation != "docker" || docker.Config.AllowUnsafeDirect {
		t.Fatal("local acknowledgement changed remote docker isolation")
	}
}

func TestMergeRejectsInvalidLocalValues(t *testing.T) {
	for name, mutate := range map[string]func(*Local){
		"plain websocket": func(l *Local) { l.ControlPlaneURL = "ws://gateway.example.test/v1/runners/stream" },
		"https":           func(l *Local) { l.ControlPlaneURL = "https://gateway.example.test" },
		"credentials":     func(l *Local) { l.ControlPlaneURL = "wss://user:pass@gateway.example.test" },
		"relative state":  func(l *Local) { l.StateDir = "state" },
		"reserved env":    func(l *Local) { l.ProviderEnvironment = map[string]map[string]string{"claude": {"GH_TOKEN": "x"}} },
		"relative binary": func(l *Local) { l.ProviderBinaries = map[string]string{"claude": "claude"} },
	} {
		local := testLocal(t)
		mutate(&local)
		if _, err := Merge(local, parsed(t, deliveryBody(dockerConfig()))); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestMergeKeepsControlPlaneURLLocal(t *testing.T) {
	merged, err := Merge(testLocal(t), parsed(t, deliveryBody(dockerConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(merged.Config.ControlPlaneURL, "wss://gateway.example.test") {
		t.Fatal("control plane URL not local")
	}
}

func TestMergeCarriesRevokedAgentPackDigests(t *testing.T) {
	config := dockerConfig()
	merged, err := Merge(testLocal(t), parsed(t, deliveryBody(config)))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Config.RevokedAgentPackDigests != nil {
		t.Fatalf("absent list merged as %v", merged.Config.RevokedAgentPackDigests)
	}
	digests := revokedTestDigests(2)
	config["revoked_agent_pack_digests"] = digests
	merged, err = Merge(testLocal(t), parsed(t, deliveryBody(config)))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Config.RevokedAgentPackDigests) != 2 || merged.Config.RevokedAgentPackDigests[0] != digests[0] || merged.Config.RevokedAgentPackDigests[1] != digests[1] {
		t.Fatalf("revoked digests = %v", merged.Config.RevokedAgentPackDigests)
	}
}
