package main

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/doctor"
)

func TestParseFlagsResolvesStateDirToAnAbsoluteCleanPath(t *testing.T) {
	working := t.TempDir()
	t.Chdir(working)
	resolvedWorking, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"./state", "state", "./nested/../state/"} {
		f, err := parseFlags([]string{"--state-dir", value})
		if err != nil {
			t.Fatal(err)
		}
		if f.stateDir != filepath.Join(resolvedWorking, "state") || !f.stateDirSet {
			t.Fatalf("%s resolved to %q", value, f.stateDir)
		}
	}
	f, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.stateDir != config.Default().StateDir || f.stateDirSet || !filepath.IsAbs(f.stateDir) {
		t.Fatalf("default state dir = %q set=%v", f.stateDir, f.stateDirSet)
	}
}

func TestDoctorConfigStillRefusesACombinedStateDir(t *testing.T) {
	deps := doctorCommandDependencies{
		load:   func(string) (config.Config, error) { return config.Default(), nil },
		run:    func(context.Context, doctor.Options) doctor.Report { return doctor.Report{} },
		output: &bytes.Buffer{},
	}
	if err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml", "--state-dir", "./relative"}, deps); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v", err)
	}
	if err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml", "--state-dir", config.Default().StateDir}, deps); err != nil {
		t.Fatalf("default state dir refused: %v", err)
	}
}

func TestRelativeStateDirLoadsTheSameConfigurationAsItsAbsoluteForm(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(h.stateDir))
	relative := "./" + filepath.Base(h.stateDir)
	loaded := map[string]doctor.Options{}
	for _, stateDir := range []string{relative, h.stateDir} {
		var captured doctor.Options
		err := cmdDoctorWithDependencies([]string{"--state-dir", stateDir}, doctorCommandDependencies{
			load: config.Load,
			loadStateDir: func(stateDir string) (config.Config, error) {
				return loadStateDirConfiguration(context.Background(), stateDir, &bytes.Buffer{})
			},
			run: func(_ context.Context, opts doctor.Options) doctor.Report {
				captured = opts
				return doctor.Report{}
			},
			output: &bytes.Buffer{},
		})
		if err != nil {
			t.Fatalf("doctor --state-dir %s: %v", stateDir, err)
		}
		loaded[stateDir] = captured
	}
	if !loaded[relative].ConfigurationLoaded || loaded[relative].StateDir != h.stateDir || loaded[relative].Provider != loaded[h.stateDir].Provider ||
		loaded[relative].ControlPlaneURL != loaded[h.stateDir].ControlPlaneURL || loaded[relative].JobImage != loaded[h.stateDir].JobImage {
		t.Fatalf("relative=%+v absolute=%+v", loaded[relative], loaded[h.stateDir])
	}
	h.respond(http.StatusUnauthorized, []byte(`{"code":"unauthorized"}`))
	for _, stateDir := range []string{relative, h.stateDir} {
		if err := cmdStart([]string{"--state-dir", stateDir}); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("start --state-dir %s err = %v", stateDir, err)
		}
	}
}
