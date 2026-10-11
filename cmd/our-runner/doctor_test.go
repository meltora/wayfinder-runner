package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/doctor"
)

func TestDoctorCommandPassesValidatedOperatorSetupFacts(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	cfg := config.Default()
	cfg.ControlPlaneURL = "https://control.example"
	cfg.StateDir = "/private/runner-state"
	cfg.Provider = "claude"
	cfg.ProviderBinaries = map[string]string{"claude": "/private/installed/claude", "codex": "/other/tool"}
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-a": {URL: "https://example.com/a"}, "repo-b": {URL: "https://example.com/b"}}
	cfg.AgentPackPublicKeys = map[string]string{"key-a": "validated"}
	var captured doctor.Options
	err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml"}, doctorCommandDependencies{
		load: func(path string) (config.Config, error) {
			if path != "operator.yaml" {
				t.Fatal("wrong config location")
			}
			return cfg, nil
		},
		run: func(_ context.Context, opts doctor.Options) doctor.Report {
			captured = opts
			return doctor.Report{Checks: []doctor.Check{{Name: "local-check-limits", Status: doctor.Warn, Detail: "execution is not checked"}}}
		},
		output: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !captured.ConfigurationLoaded || captured.Provider != "claude" || captured.ProviderBinary != "/private/installed/claude" || captured.RepositoryCount != 2 || captured.TrustedAgentPackKeyCount != 1 {
		t.Fatal("operator setup facts were not passed")
	}
	if captured.JobImage != cfg.JobImage || captured.JobNetwork != cfg.JobNetwork || captured.Isolation != cfg.Isolation || captured.StateDir != cfg.StateDir {
		t.Fatal("existing diagnostic settings changed")
	}
	if !strings.Contains(output.String(), "execution is not checked") || strings.Contains(output.String(), "/private/") {
		t.Fatal("unsafe diagnostic output")
	}
}
func TestDoctorCommandPreservesLimitedURLMode(t *testing.T) {
	t.Parallel()
	err := cmdDoctorWithDependencies([]string{"--url", "https://control.example", "--state-dir", "/operator/state"}, doctorCommandDependencies{
		load: func(string) (config.Config, error) {
			t.Fatal("limited diagnostics loaded config")
			return config.Config{}, nil
		},
		run: func(_ context.Context, opts doctor.Options) doctor.Report {
			if opts.ConfigurationLoaded || opts.Provider != "" || opts.ControlPlaneURL != "https://control.example" || opts.StateDir != "/operator/state" {
				t.Fatal("limited diagnostic settings changed")
			}
			return doctor.Report{Checks: []doctor.Check{{Name: "runner-certificate", Status: doctor.Warn, Detail: "not enrolled"}}}
		},
		output: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestDoctorCommandRejectsInvalidInvocationBeforeProbes(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--unknown", "x"}, {"--config", "operator.yaml", "--url", "https://control.example"}, {"--config", "operator.yaml", "--state-dir", "/different"}} {
		err := cmdDoctorWithDependencies(args, doctorCommandDependencies{load: func(string) (config.Config, error) {
			t.Fatal("invalid command loaded config")
			return config.Config{}, nil
		}, run: func(context.Context, doctor.Options) doctor.Report {
			t.Fatal("invalid command ran probes")
			return doctor.Report{}
		}, output: io.Discard})
		if err == nil {
			t.Fatal("invalid diagnostic command accepted")
		}
	}
}
func TestDoctorCommandPropagatesFailuresWithoutLeakingConfiguration(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"load", "check", "output"} {
		t.Run(kind, func(t *testing.T) {
			deps := doctorCommandDependencies{load: func(string) (config.Config, error) { return config.Default(), nil }, run: func(context.Context, doctor.Options) doctor.Report {
				return doctor.Report{Checks: []doctor.Check{{Name: "local", Status: doctor.OK}}}
			}, output: io.Discard}
			if kind == "load" {
				deps.load = func(string) (config.Config, error) { return config.Config{}, errors.New("/private/secret-config") }
				deps.run = func(context.Context, doctor.Options) doctor.Report {
					t.Fatal("invalid config ran probes")
					return doctor.Report{}
				}
			}
			if kind == "check" {
				deps.run = func(context.Context, doctor.Options) doctor.Report {
					return doctor.Report{Checks: []doctor.Check{{Name: "runner-certificate", Status: doctor.Fail}}}
				}
			}
			if kind == "output" {
				deps.output = failedDoctorWriter{}
			}
			err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml"}, deps)
			if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
				t.Fatal("missing or unsafe diagnostic failure")
			}
		})
	}
}

type failedDoctorWriter struct{}

func (failedDoctorWriter) Write([]byte) (int, error) { return 0, errors.New("/private/output-secret") }
func TestDoctorCommandPassesProviderEnvironmentNamesWithoutValues(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.ControlPlaneURL = "https://control.example"
	cfg.Provider = "claude"
	cfg.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": "/private/accounts/xogito"}}
	var output bytes.Buffer
	err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml"}, doctorCommandDependencies{
		load: func(string) (config.Config, error) { return cfg, nil },
		run: func(_ context.Context, opts doctor.Options) doctor.Report {
			if names := opts.ProviderEnvironmentNames["claude"]; len(names) != 1 || names[0] != "CLAUDE_CONFIG_DIR" || len(opts.ProviderEnvironmentNames["codex"]) != 0 {
				t.Fatalf("names = %v", opts.ProviderEnvironmentNames)
			}
			return doctor.RunWithDependencies(context.Background(), opts, doctor.Dependencies{
				SystemChecks: func(context.Context, doctor.Options) []doctor.Check { return nil },
				LookPath:     func(string) (string, error) { return "", errors.New("absent") },
				Stat:         func(string) (os.FileInfo, error) { return nil, errors.New("absent") },
			})
		},
		output: &output,
	})
	if err == nil {
		t.Fatal("missing runtime did not fail")
	}
	if !strings.Contains(output.String(), "provider-environment: claude: CLAUDE_CONFIG_DIR;") || strings.Contains(output.String(), "/private/") {
		t.Fatalf("output = %q", output.String())
	}
}
func TestDoctorCommandReportsPlatformTrustInsteadOfHostPackKeyAcceptance(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Provider = "claude"
	cfg.Isolation = "direct"
	cfg.AgentPackTrustedKeySet = &v1.AgentPackTrustedKeySet{DocumentBase64: "e30=", SignatureBase64: "AAAA", RootKeyID: strings.Repeat("a", 64)}
	var captured doctor.Options
	var output bytes.Buffer
	err := cmdDoctorWithDependencies([]string{"--state-dir", "/runner state"}, doctorCommandDependencies{
		load: func(string) (config.Config, error) {
			t.Fatal("state-dir diagnostics loaded a config file")
			return config.Config{}, nil
		},
		loadStateDir: func(stateDir string) (config.Config, error) {
			if stateDir != "/runner state" {
				t.Fatalf("state dir = %q", stateDir)
			}
			return cfg, nil
		},
		run: func(ctx context.Context, opts doctor.Options) doctor.Report {
			captured = opts
			return doctor.RunWithDependencies(ctx, opts, doctor.Dependencies{
				SystemChecks: func(context.Context, doctor.Options) []doctor.Check { return nil },
				LookPath:     func(string) (string, error) { return os.Args[0], nil },
				Stat:         os.Stat,
			})
		},
		output: &output,
	})
	if err != nil {
		t.Fatalf("a Warn failed the command: %v\n%s", err, output.String())
	}
	if !captured.ConfigurationLoaded || captured.PlatformTrustedKeySet != cfg.AgentPackTrustedKeySet || captured.BuildChannel != daemon.Channel {
		t.Fatalf("platform trust inputs were not passed: %+v", captured)
	}
	for _, expected := range []string{"[WARN] platform-trust: ", "[WARN] isolation: direct isolation is development-only"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output lacks %q:\n%s", expected, output.String())
		}
	}
	for _, forbidden := range []string{"agent-pack-keys-awaiting", "--accept-pack-keys", "awaiting acceptance"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("output still asks the host to accept pack keys (%q):\n%s", forbidden, output.String())
		}
	}
}
func TestDoctorConfigDoesNotResolvePackKeysFromTheStateDirectory(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Provider = "claude"
	cfg.Isolation = "direct"
	cfg.StateDir = "/operator state"
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-a": {URL: "https://example.com/a"}}
	var captured doctor.Options
	var output bytes.Buffer
	err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml"}, doctorCommandDependencies{
		load: func(string) (config.Config, error) { return cfg, nil },
		loadStateDir: func(string) (config.Config, error) {
			t.Fatal("--config diagnostics resolved the state directory")
			return config.Config{}, nil
		},
		run: func(ctx context.Context, opts doctor.Options) doctor.Report {
			captured = opts
			return doctor.RunWithDependencies(ctx, opts, doctor.Dependencies{
				SystemChecks: func(context.Context, doctor.Options) []doctor.Check { return nil },
				LookPath:     func(string) (string, error) { return os.Args[0], nil },
				Stat:         os.Stat,
			})
		},
		output: &output,
	})
	if err != nil {
		t.Fatalf("a Warn failed the command: %v\n%s", err, output.String())
	}
	if captured.Provider != "claude" || captured.RepositoryCount != 1 || captured.StateDir != "/operator state" {
		t.Fatalf("--config facts were replaced: %+v", captured)
	}
	if strings.Contains(output.String(), "agent-pack-keys-awaiting") || strings.Contains(output.String(), "--accept-pack-keys") || !strings.Contains(output.String(), "platform-trust") {
		t.Fatalf("output:\n%s", output.String())
	}
}
