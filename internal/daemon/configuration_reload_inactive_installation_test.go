package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/remoteconfig"
)

func inactiveInstallationEnvelope(t *testing.T, digest string, repositories map[string]string, installations []string, omitted []map[string]string) []byte {
	t.Helper()
	repos := map[string]any{}
	for id, url := range repositories {
		repos[id] = map[string]string{"url": url}
	}
	sources := map[string]any{}
	for _, id := range installations {
		sources[id] = map[string]string{"provider": "github"}
	}
	if omitted == nil {
		omitted = []map[string]string{}
	}
	raw, err := json.Marshal(map[string]any{
		"schema_version":   1,
		"runner_id":        "runner-1",
		"profile_revision": 1,
		"digest":           digest,
		"config": map[string]any{
			"isolation":                        "docker",
			"provider":                         "claude",
			"job_image":                        "ghcr.io/example/job@sha256:" + strings.Repeat("a", 64),
			"job_network":                      "none",
			"job_memory_mb":                    4096,
			"job_cpus":                         2,
			"job_pids":                         512,
			"labels":                           map[string]string{},
			"repositories":                     repos,
			"agent_pack_public_keys":           map[string]string{},
			"source_publication_installations": sources,
		},
		"pack_key_fingerprints": map[string]string{},
		"omitted_repositories":  omitted,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReloadDropsARepositoryAndInstallationLeftOutForAnInactiveInstallation(t *testing.T) {
	stateDir := t.TempDir()
	local := remoteconfig.Local{ControlPlaneURL: "wss://gateway.example.test/v1/runners/stream", StateDir: stateDir}
	firstDigest, secondDigest := strings.Repeat("1", 64), strings.Repeat("2", 64)
	first := inactiveInstallationEnvelope(t, firstDigest,
		map[string]string{"repo-1": "https://github.com/acme/api.git", "repo-2": "https://github.com/other/web.git"},
		[]string{"installation-1", "installation-2"}, nil)
	second := inactiveInstallationEnvelope(t, secondDigest,
		map[string]string{"repo-1": "https://github.com/acme/api.git"},
		[]string{"installation-1"},
		[]map[string]string{{"id": "repo-2", "reason": "installation_inactive"}})

	running, err := remoteconfig.ParseDelivery(first, "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := remoteconfig.Merge(local, running)
	if err != nil {
		t.Fatal(err)
	}
	cfg := merged.Config
	cfg.RunnerConfigurationDigest = firstDigest
	d := &Daemon{
		cfg:        cfg,
		store:      identity.NewStore(stateDir),
		collectors: collector.NewStore(stateDir),
		runnerID:   "runner-1",
		logger:     slog.Default(),
		eventSink:  (&capturedEvents{}).sink,
	}
	if _, err := d.store.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	if err := d.applyHotConfiguration(cfg, firstDigest); err != nil {
		t.Fatal(err)
	}
	started := d.hotConfiguration()
	if _, ok := started.sourcePublicationInstallations["installation-2"]; !ok {
		t.Fatalf("the starting configuration lacks installation-2: %+v", started)
	}

	sent := ""
	d.configurationReloader = remoteconfig.NewReloader(stateDir, "runner-1", local, running, func(_ context.Context, runningDigest string) ([]byte, error) {
		sent = runningDigest
		return second, nil
	})
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("the reload failed")
	}
	if sent != firstDigest {
		t.Fatalf("conditional digest = %q", sent)
	}
	hot := d.hotConfiguration()
	if hot.digest != secondDigest {
		t.Fatalf("digest = %q", hot.digest)
	}
	if _, ok := hot.repositories["repo-2"]; ok || len(hot.repositories) != 1 {
		t.Fatalf("repositories = %+v", hot.repositories)
	}
	if _, ok := hot.sourcePublicationInstallations["installation-2"]; ok || len(hot.sourcePublicationInstallations) != 1 {
		t.Fatalf("installations = %+v", hot.sourcePublicationInstallations)
	}
	if d.cfg.Provider != "claude" || d.cfg.Isolation != "docker" {
		t.Fatalf("restart-required settings changed: %+v", d.cfg)
	}

	jr := &jobRunner{
		d:       d,
		job:     v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", Repositories: []v1.JobRepository{{RepositoryID: "repo-2", Primary: true}}},
		workDir: t.TempDir(), logger: slog.Default(),
	}
	if _, ok := jr.jobConfiguration().sourcePublicationInstallations["installation-2"]; ok {
		t.Fatal("a new job still lists the inactive installation")
	}
	if _, _, err := jr.prepareWorkspace(context.Background()); err == nil || !strings.Contains(err.Error(), "not registered on this runner") {
		t.Fatalf("a job for the omitted repository was prepared: %v", err)
	}
}
