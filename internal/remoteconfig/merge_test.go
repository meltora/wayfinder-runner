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
	local.AcceptedPackKeyFingerprints = []string{key.fingerprint}
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
	if cfg.AgentPackPublicKeys["pack-key-1"] != key.spkiBase64 || len(merged.AwaitingPackKeys) != 0 || len(merged.AcceptedPackKeys) != 1 {
		t.Fatalf("accepted key missing: %+v", merged)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMergeDropsPackKeysAwaitingHostAcceptance(t *testing.T) {
	accepted := newTestPackKey(t, "pack-key-1")
	pending := newTestPackKey(t, "pack-key-2")
	local := testLocal(t)
	local.AcceptedPackKeyFingerprints = []string{accepted.fingerprint}
	merged, err := Merge(local, parsed(t, deliveryBody(dockerConfig(accepted, pending), accepted, pending)))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Config.AgentPackPublicKeys) != 1 || merged.Config.AgentPackPublicKeys["pack-key-1"] == "" {
		t.Fatalf("keys = %+v", merged.Config.AgentPackPublicKeys)
	}
	if len(merged.AwaitingPackKeys) != 1 || merged.AwaitingPackKeys[0].ID != "pack-key-2" || merged.AwaitingPackKeys[0].Fingerprint != pending.fingerprint {
		t.Fatalf("awaiting = %+v", merged.AwaitingPackKeys)
	}
	none, err := Merge(testLocal(t), parsed(t, deliveryBody(dockerConfig(accepted), accepted)))
	if err != nil {
		t.Fatal(err)
	}
	if len(none.Config.AgentPackPublicKeys) != 0 || len(none.AwaitingPackKeys) != 1 {
		t.Fatalf("unaccepted key was trusted: %+v", none)
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
		"fingerprint":     func(l *Local) { l.AcceptedPackKeyFingerprints = []string{"not-hex"} },
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
