package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/doctor"
	"github.com/example/app-runner/internal/mockcp"
	"github.com/example/app-runner/internal/remoteconfig"
)

const configureJobImage = "ghcr.io/example/job@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type configurePackKey struct {
	id, spki, fingerprint string
}

func newConfigurePackKey(t *testing.T, id string) configurePackKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return configurePackKey{id: id, spki: base64.StdEncoding.EncodeToString(der), fingerprint: hex.EncodeToString(sum[:])}
}

func configureDelivery(t *testing.T, runnerID, isolation string, keys ...configurePackKey) []byte {
	t.Helper()
	publicKeys := map[string]string{}
	fingerprints := map[string]string{}
	for _, key := range keys {
		publicKeys[key.id] = key.spki
		fingerprints[key.id] = key.fingerprint
	}
	document := map[string]any{
		"provider":                         "claude",
		"labels":                           map[string]string{},
		"repositories":                     map[string]any{"repo-1": map[string]string{"url": "https://github.com/acme/api.git"}},
		"agent_pack_public_keys":           publicKeys,
		"source_publication_installations": map[string]any{},
	}
	if isolation == "direct" {
		document["isolation"] = "direct"
		document["allow_unsafe_direct"] = true
	} else {
		document["isolation"] = "docker"
		document["job_image"] = configureJobImage
		document["job_network"] = "none"
		document["job_memory_mb"] = 4096
		document["job_cpus"] = 2
		document["job_pids"] = 512
	}
	raw, err := json.Marshal(map[string]any{
		"schema_version":        1,
		"runner_id":             runnerID,
		"profile_revision":      1,
		"digest":                strings.Repeat("c", 64),
		"config":                document,
		"pack_key_fingerprints": fingerprints,
		"omitted_repositories":  []any{map[string]string{"id": "repo-9", "reason": "clone_url_unavailable"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type configureHarness struct {
	server   *mockcp.Server
	stateDir string
	runnerID string
	gateway  string
	output   *bytes.Buffer
	slept    []time.Duration
}

func newConfigureHarness(t *testing.T) *configureHarness {
	t.Helper()
	server, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	code := "configure-" + strings.ReplaceAll(t.Name(), "/", "-")
	server.AddEnrollmentCode(code)
	stateDir := filepath.Join(t.TempDir(), "state")
	codeFile := filepath.Join(t.TempDir(), "code")
	if err := os.WriteFile(codeFile, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdEnroll([]string{"--state-dir", stateDir, "--code-file", codeFile, "--url", server.EnrollURL(), "--allow-insecure"}); err != nil {
		t.Fatal(err)
	}
	runnerID, err := daemon.ReadRunnerID(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return &configureHarness{server: server, stateDir: stateDir, runnerID: runnerID, gateway: server.WSURL(), output: &bytes.Buffer{}}
}

func (h *configureHarness) run(input string, terminal bool, args ...string) error {
	return cmdConfigureWithDependencies(append([]string{"--gateway", h.gateway, "--state-dir", h.stateDir}, args...), configureDependencies{
		input:    strings.NewReader(input),
		output:   h.output,
		terminal: terminal,
		newFetch: newRemoteFetch,
		sleep: func(_ context.Context, d time.Duration) error {
			h.slept = append(h.slept, d)
			return nil
		},
	})
}

func (h *configureHarness) respond(status int, body []byte) {
	h.server.SetConfigurationResponses(mockcp.ConfigurationResponse{Status: status, Body: body})
}

func (h *configureHarness) local(t *testing.T) remoteconfig.Local {
	t.Helper()
	local, err := remoteconfig.LoadLocal(h.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return local
}

func assertNoConfigurationFiles(t *testing.T, stateDir string) {
	t.Helper()
	for _, name := range []string{remoteconfig.LocalFileName, remoteconfig.RemoteFileName} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was written: %v", name, err)
		}
	}
}

func TestConfigureNonInteractiveWritesPrivateFilesAndNeverAnchorsPackKeys(t *testing.T) {
	h := newConfigureHarness(t)
	key := newConfigurePackKey(t, "pack-key-1")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
	err := h.run("", false, "--non-interactive", "--provider-env", "CLAUDE_CONFIG_DIR=/home/runner/.claude-secret-account", "--provider-binary", "/usr/local/bin/claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{remoteconfig.LocalFileName, remoteconfig.RemoteFileName} {
		info, err := os.Stat(filepath.Join(h.stateDir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
	local := h.local(t)
	if local.ControlPlaneURL != h.gateway || local.StateDir != h.stateDir ||
		local.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/home/runner/.claude-secret-account" || local.ProviderBinaries["claude"] != "/usr/local/bin/claude" || local.AcknowledgeDirect {
		t.Fatalf("local = %+v", local)
	}
	out := h.output.String()
	for _, want := range []string{h.runnerID, "docker", configureJobImage, "Provider: Claude Code", "1 included", "1 omitted", "Platform trust: no signed key set delivered yet", "CLAUDE_CONFIG_DIR", "our-runner start"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{".claude-secret-account", "BEGIN", key.spki, key.fingerprint, "pack-key-1", "Agent Pack"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("output leaked %q", forbidden)
		}
	}
	if requests := h.server.ConfigurationRequests(); len(requests) != 1 || requests[0].ClientName != h.runnerID {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestConfigureRejectsTheRetiredAcceptPackKeysFlag(t *testing.T) {
	h := newConfigureHarness(t)
	key := newConfigurePackKey(t, "pack-key-1")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
	if err := h.run("", false, "--non-interactive", "--accept-pack-keys"); err == nil || !strings.Contains(err.Error(), "unknown flag --accept-pack-keys") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.stateDir, remoteconfig.LocalFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s was written: %v", remoteconfig.LocalFileName, err)
	}
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output.String(), "awaiting host acceptance") || strings.Contains(h.output.String(), "--accept-pack-keys") {
		t.Fatalf("output:\n%s", h.output.String())
	}
}

func TestConfigureDropsLegacyAcceptedPackKeyFingerprintsFromAnUpgradedHost(t *testing.T) {
	for name, legacy := range map[string]string{
		"populated": "accepted_pack_key_fingerprints:\n  pack-key-1: " + strings.Repeat("ab", 32) + "\n",
		"empty":     "accepted_pack_key_fingerprints: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newConfigureHarness(t)
			key := newConfigurePackKey(t, "pack-key-1")
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
			if err := h.run("", false, "--non-interactive", "--provider-env", "CLAUDE_CONFIG_DIR=/home/runner/.claude"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.stateDir, remoteconfig.LocalFileName)
			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(written, []byte(legacy)...), 0o600); err != nil {
				t.Fatal(err)
			}
			if local := h.local(t); local.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/home/runner/.claude" {
				t.Fatalf("local = %+v", local)
			}
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
			if err := h.run("", false, "--non-interactive"); err != nil {
				t.Fatal(err)
			}
			rewritten, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(rewritten), "accepted_pack_key_fingerprints") {
				t.Fatalf("legacy key survived configure:\n%s", rewritten)
			}
			if local := h.local(t); local.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/home/runner/.claude" {
				t.Fatalf("local = %+v", local)
			}
		})
	}
}

func TestConfigureInteractiveAsksOnlyLocalQuestions(t *testing.T) {
	h := newConfigureHarness(t)
	key := newConfigurePackKey(t, "pack-key-1")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
	if err := h.run("/home/runner/.claude-work\n\n", true); err != nil {
		t.Fatal(err)
	}
	local := h.local(t)
	if local.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/home/runner/.claude-work" || len(local.ProviderBinaries) != 0 {
		t.Fatalf("local = %+v", local)
	}
	if strings.Contains(h.output.String(), "/home/runner/.claude-work") {
		t.Fatal("environment value was echoed")
	}
}

func TestConfigureWithoutPackKeysAsksNothingAboutKeys(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("\n\n", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output.String(), "Trust Agent Pack key") {
		t.Fatalf("asked about keys:\n%s", h.output.String())
	}
}

func TestConfigureDirectRequiresHostAcknowledgement(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "direct"))
	if err := h.run("", false, "--non-interactive"); err == nil || !strings.Contains(err.Error(), "--acknowledge-direct") {
		t.Fatalf("err = %v", err)
	}
	assertNoConfigurationFiles(t, h.stateDir)
	if err := h.run("\n\nno\n", true); err == nil {
		t.Fatal("direct accepted without typed confirmation")
	}
	assertNoConfigurationFiles(t, h.stateDir)
	if !strings.Contains(h.output.String(), "not sandboxed") {
		t.Fatalf("no warning:\n%s", h.output.String())
	}
	if err := h.run("\n\ndirect\n", true); err != nil {
		t.Fatal(err)
	}
	if !h.local(t).AcknowledgeDirect {
		t.Fatal("acknowledgement not recorded")
	}
	other := newConfigureHarness(t)
	other.respond(http.StatusOK, configureDelivery(t, other.runnerID, "direct"))
	if err := other.run("", false, "--non-interactive", "--acknowledge-direct"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureWaitsForTheDashboardProfile(t *testing.T) {
	h := newConfigureHarness(t)
	h.server.SetConfigurationResponses(
		mockcp.ConfigurationResponse{Status: 404, Body: []byte(`{"code":"configuration_not_ready"}`)},
		mockcp.ConfigurationResponse{Status: 404, Body: []byte(`{"code":"configuration_not_ready"}`)},
		mockcp.ConfigurationResponse{Status: 200, Body: configureDelivery(t, h.runnerID, "docker")},
	)
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(h.output.String(), "Waiting for the dashboard to save this runner's configuration") != 1 || len(h.slept) != 2 || h.slept[0] != 5*time.Second {
		t.Fatalf("slept=%v output:\n%s", h.slept, h.output.String())
	}
}

func TestConfigureRefusesAndWritesNothingOnDenialsOrInvalidMerges(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusUnauthorized, []byte(`{"code":"unauthorized"}`))
	if err := h.run("", false, "--non-interactive"); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("err = %v", err)
	}
	h.respond(http.StatusConflict, []byte(`{"code":"direct_isolation_unavailable"}`))
	if err := h.run("", false, "--non-interactive"); err == nil || !strings.Contains(err.Error(), "development") {
		t.Fatalf("err = %v", err)
	}
	h.respond(http.StatusOK, configureDelivery(t, "runner-someone-else", "docker"))
	if err := h.run("", false, "--non-interactive"); err == nil {
		t.Fatal("accepted another runner's document")
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive", "--provider-env", "GH_TOKEN=secret-token-value"); err == nil || strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("err = %v", err)
	}
	assertNoConfigurationFiles(t, h.stateDir)
}

func TestConfigureRequiresEnrollmentATerminalAndValidFlags(t *testing.T) {
	output := &bytes.Buffer{}
	fetched := false
	deps := configureDependencies{input: strings.NewReader(""), output: output, terminal: false, newFetch: func(string, string) (remoteconfig.FetchFunc, error) {
		fetched = true
		return nil, errors.New("unexpected fetch")
	}}
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := cmdConfigureWithDependencies([]string{"--gateway", "wss://gateway.example.test", "--state-dir", stateDir}, deps); err == nil || !strings.Contains(err.Error(), "not enrolled; run our-runner enroll first") {
		t.Fatalf("err = %v", err)
	}
	h := newConfigureHarness(t)
	if err := cmdConfigureWithDependencies([]string{"--gateway", h.gateway, "--state-dir", h.stateDir}, deps); err == nil ||
		!strings.Contains(err.Error(), "--non-interactive") || strings.Contains(err.Error(), "--accept-pack-keys") || !strings.Contains(err.Error(), "--provider-env") {
		t.Fatalf("err = %v", err)
	}
	for _, args := range [][]string{
		{},
		{"--gateway", "https://gateway.example.test"},
		{"--gateway", "wss://gateway.example.test", "--gateway", "wss://other.example.test"},
		{"--gateway", "wss://gateway.example.test", "--provider-binary", "claude"},
		{"--gateway", "wss://gateway.example.test", "--provider-env", "NOVALUE"},
		{"--gateway", "wss://gateway.example.test", "--provider-env", "A=1", "--provider-env", "A=2"},
		{"--gateway", "wss://gateway.example.test", "--unknown"},
		{"--gateway"},
	} {
		if err := cmdConfigureWithDependencies(args, deps); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if fetched {
		t.Fatal("fetched before validating the invocation")
	}
}

func TestStateDirModesLoadTheMergedConfiguration(t *testing.T) {
	h := newConfigureHarness(t)
	key := newConfigurePackKey(t, "pack-key-1")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	var captured doctor.Options
	output := &bytes.Buffer{}
	err := cmdDoctorWithDependencies([]string{"--state-dir", h.stateDir}, doctorCommandDependencies{
		load: func(string) (config.Config, error) {
			t.Fatal("state-dir mode loaded a config file")
			return config.Config{}, nil
		},
		loadStateDir: func(stateDir string) (config.Config, error) {
			return loadStateDirConfiguration(context.Background(), stateDir, output)
		},
		run: func(_ context.Context, opts doctor.Options) doctor.Report {
			captured = opts
			return doctor.Report{}
		},
		output: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !captured.ConfigurationLoaded || captured.Provider != "claude" || captured.RepositoryCount != 1 || captured.TrustedAgentPackKeyCount != 0 ||
		captured.StateDir != h.stateDir || captured.ControlPlaneURL != h.gateway || captured.JobImage != configureJobImage {
		t.Fatalf("options = %+v", captured)
	}
	h.respond(http.StatusServiceUnavailable, []byte(`{"code":"unavailable"}`))
	warnings := &bytes.Buffer{}
	cfg, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings)
	if err != nil || cfg.Provider != "claude" || !strings.Contains(warnings.String(), "cached runner configuration") {
		t.Fatalf("cfg=%+v err=%v warnings=%s", cfg, err, warnings.String())
	}
	h.respond(http.StatusUnauthorized, []byte(`{"code":"unauthorized"}`))
	if _, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("err = %v", err)
	}
	if err := cmdStart([]string{"--state-dir", h.stateDir}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("start err = %v", err)
	}
}

func TestStateDirModesExplainMissingConfiguration(t *testing.T) {
	h := newConfigureHarness(t)
	if err := cmdStart([]string{"--state-dir", h.stateDir}); err == nil || !strings.Contains(err.Error(), "run our-runner configure first") {
		t.Fatalf("start err = %v", err)
	}
	err := cmdDoctorWithDependencies([]string{"--state-dir", h.stateDir}, doctorCommandDependencies{
		load: config.Load,
		loadStateDir: func(stateDir string) (config.Config, error) {
			return loadStateDirConfiguration(context.Background(), stateDir, &bytes.Buffer{})
		},
		run:    func(context.Context, doctor.Options) doctor.Report { return doctor.Report{} },
		output: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "run our-runner configure first") {
		t.Fatalf("doctor err = %v", err)
	}
	if err := cmdStart([]string{"--config", filepath.Join(t.TempDir(), "runner.yaml"), "--state-dir", h.stateDir}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("combined err = %v", err)
	}
}

func TestConfigureSummaryBreaksDownOmittedRepositories(t *testing.T) {
	omitted := func(reasons ...string) []remoteconfig.OmittedRepository {
		out := make([]remoteconfig.OmittedRepository, 0, len(reasons))
		for index, reason := range reasons {
			out = append(out, remoteconfig.OmittedRepository{ID: "omitted-" + string(rune('a'+index)), Reason: reason})
		}
		return out
	}
	included := func(count int) map[string]string {
		out := map[string]string{}
		for index := 0; index < count; index++ {
			out["repo-"+string(rune('a'+index))] = "https://github.com/acme/r.git"
		}
		return out
	}
	for name, test := range map[string]struct {
		included int
		omitted  []remoteconfig.OmittedRepository
		want     string
	}{
		"none omitted": {3, nil, "Repositories: 3 included, 0 omitted\n"},
		"both kinds": {3, omitted("clone_url_unavailable", "installation_inactive"),
			"Repositories: 3 included, 2 omitted (1 without a clone URL, 1 on a suspended or revoked GitHub installation)\n"},
		"inactive only": {0, omitted("installation_inactive"),
			"Repositories: 0 included, 1 omitted (1 on a suspended or revoked GitHub installation)\n"},
		"clone URL only": {1, omitted("clone_url_unavailable", "clone_url_unavailable"),
			"Repositories: 1 included, 2 omitted (2 without a clone URL)\n"},
	} {
		var output bytes.Buffer
		printConfigureSummary(&output, remoteconfig.Delivery{
			RunnerID:            "runner-1",
			ProfileRevision:     1,
			Remote:              remoteconfig.Remote{Isolation: "docker", JobImage: configureJobImage, Provider: "claude", Repositories: included(test.included)},
			OmittedRepositories: test.omitted,
		})
		lines := strings.SplitAfter(output.String(), "\n")
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, "Repositories:") {
				found = true
				if line != test.want {
					t.Fatalf("%s: line = %q, want %q", name, line, test.want)
				}
			}
		}
		if !found {
			t.Fatalf("%s: no repositories line in %q", name, output.String())
		}
	}
}
