package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadNeverReadsARunnerConfigurationDigest(t *testing.T) {
	dir := t.TempDir()
	base := "control_plane_url: wss://cp.example.com/stream\ntransport: websocket\njob_image: registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunnerConfigurationDigest != "" {
		t.Fatalf("--config mode reported digest %q", cfg.RunnerConfigurationDigest)
	}
	injected := filepath.Join(dir, "injected.yaml")
	if err := os.WriteFile(injected, []byte(base+"runner_configuration_digest: "+strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(injected); err == nil {
		t.Fatal("a local config file may not claim a remote configuration digest")
	}
}
