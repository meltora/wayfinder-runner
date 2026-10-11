package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestStateDirConfigurationCarriesTheDigestItIsRunning(t *testing.T) {
	h := newConfigureHarness(t)
	key := newConfigurePackKey(t, "pack-key-1")
	delivered := configureDelivery(t, h.runnerID, "docker", key)
	h.respond(http.StatusOK, delivered)
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	fresh := strings.Repeat("e", 64)
	h.respond(http.StatusOK, bytes.Replace(delivered, []byte(strings.Repeat("c", 64)), []byte(fresh), 1))
	cfg, err := loadStateDirConfiguration(context.Background(), h.stateDir, &bytes.Buffer{})
	if err != nil || cfg.RunnerConfigurationDigest != fresh {
		t.Fatalf("fresh digest = %q err=%v", cfg.RunnerConfigurationDigest, err)
	}
	h.respond(http.StatusServiceUnavailable, []byte(`{"code":"unavailable"}`))
	warnings := &bytes.Buffer{}
	cfg, err = loadStateDirConfiguration(context.Background(), h.stateDir, warnings)
	if err != nil || !strings.Contains(warnings.String(), "cached runner configuration") || cfg.RunnerConfigurationDigest != fresh {
		t.Fatalf("cached digest = %q err=%v warnings=%s", cfg.RunnerConfigurationDigest, err, warnings.String())
	}
}
