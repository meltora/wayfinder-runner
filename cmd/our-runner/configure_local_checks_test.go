package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/doctor"
	"github.com/example/app-runner/internal/remoteconfig"
)

const configureLocalChecksYAML = "checks:\n  go-test:\n    command: go\n    args: [test, ./...]\n    timeout_seconds: 600\n    repository_id: repo-1\n" +
	"  web-unit:\n    command: npm\n    args: [test]\n    install:\n      ecosystem: npm\n      directories: [web]\n      timeout_seconds: 300\n"

func configureLocalChecks() map[string]config.CheckConfig {
	return map[string]config.CheckConfig{
		"go-test":  {Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 600, RepositoryID: "repo-1"},
		"web-unit": {Command: "npm", Args: []string{"test"}, Install: &config.CheckInstallConfig{Ecosystem: "npm", Directories: []string{"web"}, TimeoutSeconds: 300}},
	}
}

func appendToLocalFile(t *testing.T, stateDir, extra string) {
	t.Helper()
	path := filepath.Join(stateDir, remoteconfig.LocalFileName)
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(written, []byte(extra)...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStateDirModeRunsTheLocalChecksAndConfigureKeepsThem(t *testing.T) {
	for _, isolation := range []string{"docker", "direct"} {
		t.Run(isolation, func(t *testing.T) {
			h := newConfigureHarness(t)
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, isolation))
			if err := h.run("", false, "--non-interactive", "--acknowledge-direct"); err != nil {
				t.Fatal(err)
			}
			appendToLocalFile(t, h.stateDir, configureLocalChecksYAML)
			want := config.Normalize(config.Config{Checks: configureLocalChecks()}).Checks
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, isolation))
			warnings := &bytes.Buffer{}
			cfg, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Isolation != isolation || !reflect.DeepEqual(cfg.Checks, want) || warnings.Len() != 0 {
				t.Fatalf("isolation = %s checks = %+v warnings = %s", cfg.Isolation, cfg.Checks, warnings.String())
			}
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, isolation))
			if err := h.run("", false, "--non-interactive"); err != nil {
				t.Fatal(err)
			}
			if local := h.local(t); !reflect.DeepEqual(local.Checks, configureLocalChecks()) {
				t.Fatalf("configure dropped the local checks: %+v", local.Checks)
			}
		})
	}
}

func TestStateDirModeWarnsAboutALocalCheckWhoseRepositoryIsNotDelivered(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	appendToLocalFile(t, h.stateDir, "checks:\n  go-test:\n    command: go\n    repository_id: repo-gone\n")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	warnings := &bytes.Buffer{}
	cfg, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Checks) != 0 {
		t.Fatalf("checks = %+v", cfg.Checks)
	}
	if !strings.Contains(warnings.String(), "local check go-test is not registered") || !strings.Contains(warnings.String(), "repo-gone") {
		t.Fatalf("warnings = %s", warnings.String())
	}
}

func TestStateDirModeWarnsThatAConfigFileInTheStateDirectoryIsIgnored(t *testing.T) {
	for name, test := range map[string]struct {
		configYAML string
		localYAML  string
		checks     bool
	}{
		"checks only in config.yaml": {configYAML: "isolation: direct\nchecks:\n  go-test:\n    command: go\n", checks: true},
		"checks in both":             {configYAML: "checks:\n  go-test:\n    command: go\n", localYAML: "checks:\n  lint:\n    command: lint\n"},
		"no checks":                  {configYAML: "isolation: direct\n"},
		"unreadable yaml":            {configYAML: "checks: [\n"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newConfigureHarness(t)
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
			if err := h.run("", false, "--non-interactive"); err != nil {
				t.Fatal(err)
			}
			appendToLocalFile(t, h.stateDir, test.localYAML)
			configPath := filepath.Join(h.stateDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(test.configYAML), 0o600); err != nil {
				t.Fatal(err)
			}
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
			output := &bytes.Buffer{}
			err := cmdDoctorWithDependencies([]string{"--state-dir", h.stateDir}, doctorCommandDependencies{
				load: func(string) (config.Config, error) {
					t.Fatal("state-dir mode loaded a config file")
					return config.Config{}, nil
				},
				loadStateDir: func(stateDir string) (config.Config, error) {
					return loadStateDirConfiguration(context.Background(), stateDir, output)
				},
				run:    func(context.Context, doctor.Options) doctor.Report { return doctor.Report{} },
				output: output,
			})
			if err != nil {
				t.Fatal(err)
			}
			text := output.String()
			if !strings.Contains(text, configPath+" is not read with --state-dir") {
				t.Fatalf("output = %s", text)
			}
			if mentions := strings.Contains(text, "its checks (go-test) are not registered"); mentions != test.checks {
				t.Fatalf("checks named = %v, output = %s", mentions, text)
			}
		})
	}
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	warnings := &bytes.Buffer{}
	if _, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings); err != nil || warnings.Len() != 0 {
		t.Fatalf("warnings without a config.yaml = %q err = %v", warnings.String(), err)
	}
}

func TestStateDirModeWarnsAboutAnIgnoredConfigFileWhenTheConfigurationFails(t *testing.T) {
	for name, test := range map[string]struct {
		localYAML string
		checks    bool
	}{
		"invalid local check":   {localYAML: "checks:\n  lint:\n    command: \"\"\n"},
		"unreadable local file": {localYAML: "retries: 3\n"},
		"invalid check id":      {localYAML: "checks:\n  \"bad id!\":\n    command: lint\n"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newConfigureHarness(t)
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
			if err := h.run("", false, "--non-interactive"); err != nil {
				t.Fatal(err)
			}
			appendToLocalFile(t, h.stateDir, test.localYAML)
			configPath := filepath.Join(h.stateDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte("checks:\n  go-test:\n    command: go\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
			warnings := &bytes.Buffer{}
			if _, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings); err == nil {
				t.Fatal("an invalid runner.local.yaml was accepted")
			}
			text := warnings.String()
			if strings.Count(text, configPath+" is not read with --state-dir") != 1 {
				t.Fatalf("warnings = %s", text)
			}
			if mentions := strings.Contains(text, "its checks (go-test) are not registered"); mentions != test.checks {
				t.Fatalf("checks named = %v, warnings = %s", mentions, text)
			}
		})
	}
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(h.stateDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("checks:\n  go-test:\n    command: go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.respond(http.StatusForbidden, []byte(`{"error":"forbidden"}`))
	warnings := &bytes.Buffer{}
	if _, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings); err == nil {
		t.Fatal("a refused configuration fetch was accepted")
	}
	if text := warnings.String(); strings.Count(text, configPath+" is not read with --state-dir") != 1 ||
		!strings.Contains(text, "its checks (go-test) are not registered: move them to "+filepath.Join(h.stateDir, remoteconfig.LocalFileName)) {
		t.Fatalf("warnings after a refused fetch = %s", text)
	}
}
