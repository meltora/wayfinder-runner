package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/remoteconfig"
)

func TestStateDirModeRunsTheLocalChecksAndConfigureKeepsThem(t *testing.T) {
	for _, isolation := range []string{"docker", "direct"} {
		t.Run(isolation, func(t *testing.T) {
			h := newConfigureHarness(t)
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, isolation))
			if err := h.run("", false, "--non-interactive", "--acknowledge-direct"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.stateDir, remoteconfig.LocalFileName)
			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			checks := "checks:\n  go-test:\n    command: go\n    args: [test, ./...]\n    timeout_seconds: 600\n    repository_id: repo-1\n"
			if err := os.WriteFile(path, append(written, []byte(checks)...), 0o600); err != nil {
				t.Fatal(err)
			}
			want := map[string]config.CheckConfig{"go-test": {Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 600, RepositoryID: "repo-1"}}
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, isolation))
			cfg, err := loadStateDirConfiguration(context.Background(), h.stateDir, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Isolation != isolation || !reflect.DeepEqual(cfg.Checks, want) {
				t.Fatalf("isolation = %s checks = %+v", cfg.Isolation, cfg.Checks)
			}
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, isolation))
			if err := h.run("", false, "--non-interactive"); err != nil {
				t.Fatal(err)
			}
			if local := h.local(t); !reflect.DeepEqual(local.Checks, want) {
				t.Fatalf("configure dropped the local checks: %+v", local.Checks)
			}
		})
	}
}
