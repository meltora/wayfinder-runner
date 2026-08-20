package config

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func productionConfig() Config {
	cfg := Default()
	cfg.ControlPlaneURL = "wss://cp.example.com/v1/runners/stream"
	cfg.JobImage = "registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return cfg
}

func testPublicKey(t *testing.T) string {
	t.Helper()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func testECDSAPublicKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func TestLoadValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("control_plane_url: wss://cp.example.com/stream\ntransport: websocket\njob_image: registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ControlPlaneURL != "wss://cp.example.com/stream" {
		t.Fatalf("url = %q", cfg.ControlPlaneURL)
	}
	if cfg.StateDir == "" {
		t.Fatal("state dir should default")
	}
	if cfg.Isolation != "docker" {
		t.Fatalf("default isolation = %q, want docker", cfg.Isolation)
	}
}

func TestDirectIsolationRequiresExplicitUnsafeOptIn(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ControlPlaneURL = "wss://cp.example.com/stream"
	cfg.Isolation = "direct"
	if err := cfg.Validate(); err == nil {
		t.Fatal("direct isolation must require allow_unsafe_direct")
	}
	cfg.AllowUnsafeDirect = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit development opt-in rejected: %v", err)
	}
}

func TestDockerRequiresImmutableImage(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ControlPlaneURL = "wss://cp.example.com/stream"
	cfg.JobImage = "app-runner-job:latest"
	if err := cfg.Validate(); err == nil {
		t.Fatal("mutable production image must be rejected")
	}
	cfg.JobImage = "registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("immutable image rejected: %v", err)
	}
}

func TestLoadMissingURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("transport: websocket\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for missing control_plane_url")
	}
}

func TestLoadBadTransport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("control_plane_url: wss://x\ntransport: pigeon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for bad transport")
	}
}

func TestServiceURLsFailClosed(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ControlPlaneURL = "ws://cp.example.com/stream"
	cfg.JobImage = "registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := cfg.Validate(); err == nil {
		t.Fatal("insecure WebSocket URL accepted")
	}
	cfg.ControlPlaneURL = "wss://user:pass@cp.example.com/stream"
	if err := cfg.Validate(); err == nil {
		t.Fatal("URL credentials accepted")
	}
	cfg.ControlPlaneURL = "wss://cp.example.com/stream?token=secret"
	if err := cfg.Validate(); err == nil {
		t.Fatal("URL query secret accepted")
	}
	cfg.ControlPlaneURL = "wss://cp.example.com/stream"
}

func TestRestrictedNetworkRequiresPolicyBinding(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ControlPlaneURL = "wss://cp.example.com/stream"
	cfg.JobImage = "registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.JobNetwork = "restricted"
	if err := cfg.Validate(); err == nil {
		t.Fatal("restricted network without binding was accepted")
	}
	cfg.JobNetworkName = "runner-egress"
	cfg.EgressPolicyID = "github-openai-v1"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid restricted network rejected: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/config.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadRejectsUnknownFieldsAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("control_plane_url: wss://cp.example.com/stream\nunknown_secret: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown config field accepted")
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("config symlink accepted")
	}
}

func TestRunnerConfigRejectsPrivilegedUpdaterTrust(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := "control_plane_url: wss://cp.example.com/stream\n" +
		"job_image: registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"update_manifest_url: https://updates.example.com/manifest.json\n" +
		"update_pubkey_b64: attacker-controlled\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("runner config accepted root updater trust or release location")
	}
	if got := Default().UpdateSocketPath; got != "/run/our-runner-updater/control.sock" {
		t.Fatalf("default update socket=%q", got)
	}
}

func TestLoadAllowsReadOnlyServiceGroupButRejectsWritableGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte("control_plane_url: wss://cp.example.com/stream\njob_image: registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("0640 config rejected: %v", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("group-writable config accepted")
	}
}

func TestValidateRejectsInvalidProductionSettings(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Config){
		"relative state":         func(c *Config) { c.StateDir = "relative" },
		"unsafe state root":      func(c *Config) { c.StateDir = "/" },
		"transport":              func(c *Config) { c.Transport = "longpoll" },
		"supervisor":             func(c *Config) { c.Supervisor = "screen" },
		"isolation":              func(c *Config) { c.Isolation = "none" },
		"provider":               func(c *Config) { c.Provider = "unknown" },
		"provider binary key":    func(c *Config) { c.ProviderBinaries = map[string]string{"unknown": "/bin/tool"} },
		"provider binary path":   func(c *Config) { c.ProviderBinaries = map[string]string{"codex": "codex"} },
		"memory":                 func(c *Config) { c.JobMemoryMB = 64 },
		"cpu":                    func(c *Config) { c.JobCPUs = -1 },
		"pids":                   func(c *Config) { c.JobPIDs = 8 },
		"artifact spool":         func(c *Config) { c.ArtifactSpoolMB = 1 },
		"retention":              func(c *Config) { c.JournalRetentionDays = 1 },
		"journal size":           func(c *Config) { c.JournalMaxMB = 1 },
		"network":                func(c *Config) { c.JobNetwork = "internet" },
		"orphan network name":    func(c *Config) { c.JobNetworkName = "runner-egress" },
		"relative update socket": func(c *Config) { c.UpdateSocketPath = "run/updater.sock" },
		"unclean update socket":  func(c *Config) { c.UpdateSocketPath = "/run/../tmp/updater.sock" },
		"agent pack key id":      func(c *Config) { c.AgentPackPublicKeys = map[string]string{"../bad": "bad"} },
		"artifact origin":        func(c *Config) { c.AllowedArtifactOrigins = []string{"http://artifacts.example.com"} },
		"repository id": func(c *Config) {
			c.Repositories = map[string]RepositoryConfig{"../bad": {URL: "https://git.example.com/repo"}}
		},
		"repository URL":   func(c *Config) { c.Repositories = map[string]RepositoryConfig{"repo": {URL: "file:///tmp/repo"}} },
		"check id":         func(c *Config) { c.Checks = map[string]CheckConfig{"../bad": {Command: "go"}} },
		"check command":    func(c *Config) { c.Checks = map[string]CheckConfig{"unit": {}} },
		"check timeout":    func(c *Config) { c.Checks = map[string]CheckConfig{"unit": {Command: "go", TimeoutSeconds: 2000}} },
		"check repository": func(c *Config) { c.Checks = map[string]CheckConfig{"unit": {Command: "go", RepositoryID: "missing"}} },
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := productionConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestValidateCompleteProductionConfiguration(t *testing.T) {
	key := testPublicKey(t)
	cfg := productionConfig()
	cfg.ControlPlaneHTTPURL = "https://cp.example.com"
	cfg.ProviderBinaries = map[string]string{"codex": "/usr/local/bin/codex"}
	cfg.JobNetwork = "restricted"
	cfg.JobNetworkName = "runner-egress"
	cfg.EgressPolicyID = "provider-v1"
	cfg.UpdateSocketPath = "/run/our-runner-updater/control.sock"
	cfg.AgentPackPublicKeys = map[string]string{"platform-v1": key}
	cfg.AllowedArtifactOrigins = []string{"https://artifacts.example.com"}
	cfg.Repositories = map[string]RepositoryConfig{"web": {URL: "ssh://git@git.example.com/company/web.git"}}
	cfg.Checks = map[string]CheckConfig{"unit": {Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 300, RepositoryID: "web"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
}

func TestValidateRejectsLegacyECDSAAgentPackTrustKey(t *testing.T) {
	cfg := productionConfig()
	cfg.AgentPackPublicKeys = map[string]string{"legacy-key": testECDSAPublicKey(t)}
	if err := cfg.Validate(); err == nil {
		t.Fatal("legacy ECDSA key accepted for signed delivery authority")
	}
}

func TestValidateRejectsTooManyAgentPackTrustKeys(t *testing.T) {
	key := testPublicKey(t)
	cfg := productionConfig()
	cfg.AgentPackPublicKeys = make(map[string]string, MaxAgentPackPublicKeys+1)
	for index := 0; index <= MaxAgentPackPublicKeys; index++ {
		cfg.AgentPackPublicKeys[fmt.Sprintf("key-%03d", index)] = key
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("excessive Agent Pack trust set accepted")
	}
}

func TestAllowedArtifactOriginsRemainCanonicalHTTPSInUnsafeDirectMode(t *testing.T) {
	for _, origin := range []string{
		"http://artifacts.example.test",
		"https://artifacts.example.test/path",
		"https://ARTIFACTS.example.test",
		"https://artifacts.example.test.:443",
		"https://café.example.test",
	} {
		cfg := productionConfig()
		cfg.Isolation = "direct"
		cfg.AllowUnsafeDirect = true
		cfg.AllowedArtifactOrigins = []string{origin}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("non-canonical artifact origin %q accepted", origin)
		}
	}
}

func TestNormalizeCopiesMutableFields(t *testing.T) {
	in := Config{
		ProviderBinaries:       map[string]string{"codex": "/bin/codex"},
		Labels:                 map[string]string{"pool": "one"},
		AgentPackPublicKeys:    map[string]string{"key": "value"},
		Repositories:           map[string]RepositoryConfig{"repo": {URL: "https://git.example.com/repo"}},
		Checks:                 map[string]CheckConfig{"unit": {Command: "go", Args: []string{"test"}}},
		AllowedArtifactOrigins: []string{"https://artifacts.example.com"},
		SourcePublicationInstallations: map[string]SourcePublicationInstallationConfig{
			"github-main": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: "/run/secrets/github-main"},
		},
	}
	out := Normalize(in)
	out.ProviderBinaries["codex"] = "changed"
	out.Labels["pool"] = "changed"
	out.AgentPackPublicKeys["key"] = "changed"
	out.Repositories["repo"] = RepositoryConfig{URL: "changed"}
	check := out.Checks["unit"]
	check.Args[0] = "changed"
	out.AllowedArtifactOrigins[0] = "changed"
	out.SourcePublicationInstallations["github-main"] = SourcePublicationInstallationConfig{Provider: "github", TokenFile: "/changed"}
	if in.ProviderBinaries["codex"] != "/bin/codex" || in.Labels["pool"] != "one" ||
		in.AgentPackPublicKeys["key"] != "value" || in.Repositories["repo"].URL != "https://git.example.com/repo" ||
		in.Checks["unit"].Args[0] != "test" || in.AllowedArtifactOrigins[0] != "https://artifacts.example.com" ||
		in.SourcePublicationInstallations["github-main"].TokenFile != "/run/secrets/github-main" {
		t.Fatal("Normalize mutated the caller's configuration")
	}
}

func TestSourcePublicationInstallationsValidateStrictLocalAuthority(t *testing.T) {
	t.Parallel()
	valid := SourcePublicationInstallationConfig{
		Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: "/run/secrets/github-main",
	}
	for name, mutate := range map[string]func(*Config){
		"installation id": func(cfg *Config) {
			cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"../escape": valid}
		},
		"unsupported provider": func(cfg *Config) {
			candidate := valid
			candidate.Provider = "unknown"
			cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"installation-1": candidate}
		},
		"insecure API": func(cfg *Config) {
			candidate := valid
			candidate.APIBaseURL = "http://api.github.com"
			cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"installation-1": candidate}
		},
		"API credentials": func(cfg *Config) {
			candidate := valid
			candidate.APIBaseURL = "https://token@api.github.com"
			cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"installation-1": candidate}
		},
		"API query": func(cfg *Config) {
			candidate := valid
			candidate.APIBaseURL = "https://api.github.com?token=secret"
			cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"installation-1": candidate}
		},
		"relative token file": func(cfg *Config) {
			candidate := valid
			candidate.TokenFile = "token"
			cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"installation-1": candidate}
		},
		"unclean token file": func(cfg *Config) {
			candidate := valid
			candidate.TokenFile = "/run/secrets/../token"
			cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"installation-1": candidate}
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := productionConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid source publication installation accepted")
			}
		})
	}

	cfg := productionConfig()
	cfg.SourcePublicationInstallations = map[string]SourcePublicationInstallationConfig{"installation-1": valid}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid source publication installation rejected: %v", err)
	}
}
