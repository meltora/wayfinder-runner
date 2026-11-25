package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

func TestConfigurePreviewHostingTurnsItOnKeepsItAndTurnsItOff(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive", "--preview-hosting", "on"); err != nil {
		t.Fatal(err)
	}
	if local := h.local(t); !local.PreviewHosting.Enabled {
		t.Fatalf("preview hosting = %+v", local.PreviewHosting)
	}
	info, err := os.Stat(filepath.Join(h.stateDir, "runner.local.yaml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("local file mode = %v, %v", info, err)
	}
	if !strings.Contains(h.output.String(), "Preview hosting: on") {
		t.Fatalf("output = %s", h.output.String())
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	cfg, err := loadStateDirConfiguration(context.Background(), h.stateDir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PreviewHosting.Enabled || cfg.PreviewHosting.MaxConcurrent != config.DefaultPreviewHostingMaxConcurrent {
		t.Fatalf("started with preview hosting = %+v", cfg.PreviewHosting)
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	if local := h.local(t); !local.PreviewHosting.Enabled {
		t.Fatal("configure without --preview-hosting dropped the saved setting")
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive", "--preview-hosting", "off"); err != nil {
		t.Fatal(err)
	}
	if local := h.local(t); local.PreviewHosting.Enabled {
		t.Fatal("--preview-hosting off left it enabled")
	}
}

func TestConfigurePreviewHostingKeepsTheSavedLimitsWhenTurnedOn(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	appendToLocalFile(t, h.stateDir, "preview_hosting:\n    max_concurrent: 2\n    memory_budget_mb: 8192\n")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive", "--preview-hosting", "on"); err != nil {
		t.Fatal(err)
	}
	if hosting := h.local(t).PreviewHosting; !hosting.Enabled || hosting.MaxConcurrent != 2 || hosting.MemoryBudgetMB != 8192 {
		t.Fatalf("preview hosting = %+v", hosting)
	}
}

func TestConfigurePreviewHostingRefusesDirectIsolationAndSavesNothing(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "direct"))
	err := h.run("", false, "--non-interactive", "--acknowledge-direct", "--preview-hosting", "on")
	if err == nil || !strings.Contains(err.Error(), "preview_hosting requires docker isolation") {
		t.Fatalf("err = %v", err)
	}
	assertNoConfigurationFiles(t, h.stateDir)
}

func TestConfigurePreviewHostingFlagValues(t *testing.T) {
	for _, args := range [][]string{
		{"--preview-hosting"},
		{"--preview-hosting", "yes"},
		{"--preview-hosting", "on", "--preview-hosting", "off"},
	} {
		if _, err := parseConfigureFlags(args); err == nil {
			t.Fatalf("%v: accepted", args)
		}
	}
}
