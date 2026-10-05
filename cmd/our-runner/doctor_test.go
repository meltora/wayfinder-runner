package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/doctor"
	"github.com/example/app-runner/internal/remoteconfig"
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
func TestDoctorCommandReportsAgentPackKeysAwaitingAcceptance(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Provider = "claude"
	cfg.Isolation = "direct"
	awaiting := []doctor.AwaitingAgentPackKey{{ID: "platform-key-2", Fingerprint: strings.Repeat("ab", 32)}}
	command := "our-runner configure --state-dir '/runner state' --accept-pack-keys"
	var captured doctor.Options
	var output bytes.Buffer
	err := cmdDoctorWithDependencies([]string{"--state-dir", "/runner state"}, doctorCommandDependencies{
		load: func(string) (config.Config, error) {
			t.Fatal("state-dir diagnostics loaded a config file")
			return config.Config{}, nil
		},
		loadStateDirAwaiting: func(stateDir string) (config.Config, []doctor.AwaitingAgentPackKey, string, error) {
			if stateDir != "/runner state" {
				t.Fatalf("state dir = %q", stateDir)
			}
			return cfg, awaiting, command, nil
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
	if !captured.ConfigurationLoaded || len(captured.AwaitingAgentPackKeys) != 1 || captured.AcceptAgentPackKeysCommand != command {
		t.Fatalf("awaiting keys were not passed: %+v", captured)
	}
	for _, expected := range []string{
		"[WARN] agent-pack-keys-awaiting: ",
		"platform-key-2 (sha256:" + strings.Repeat("ab", 32) + ")",
		command,
		"[WARN] isolation: direct isolation is development-only",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output lacks %q:\n%s", expected, output.String())
		}
	}
}
func TestStateDirAcceptPackKeysCommandQuotesTheStateDirectory(t *testing.T) {
	t.Parallel()
	got := acceptPackKeysCommand("https://gateway.example", false, "/runner's state")
	want := "our-runner configure --gateway 'https://gateway.example' --state-dir '/runner'\\''s state' --accept-pack-keys"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if stored := acceptPackKeysCommand("https://gateway.example", true, "/state"); stored != "our-runner configure --state-dir '/state' --accept-pack-keys" {
		t.Fatalf("stored gateway command = %q", stored)
	}
}

func TestDoctorConfigResolvesAwaitingAgentPackKeysFromItsStateDirectory(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Provider = "claude"
	cfg.Isolation = "direct"
	cfg.StateDir = "/operator state"
	cfg.Repositories = map[string]config.RepositoryConfig{"repo-a": {URL: "https://example.com/a"}}
	stateDirConfig := config.Default()
	stateDirConfig.Provider = "codex"
	awaiting := []doctor.AwaitingAgentPackKey{{ID: "platform-key-2", Fingerprint: strings.Repeat("ab", 32)}}
	command := "our-runner configure --state-dir '/operator state' --accept-pack-keys"
	var captured doctor.Options
	var output bytes.Buffer
	err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml"}, doctorCommandDependencies{
		load: func(string) (config.Config, error) { return cfg, nil },
		loadStateDirAwaiting: func(stateDir string) (config.Config, []doctor.AwaitingAgentPackKey, string, error) {
			if stateDir != "/operator state" {
				t.Fatalf("state dir = %q", stateDir)
			}
			return stateDirConfig, awaiting, command, nil
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
	if len(captured.AwaitingAgentPackKeys) != 1 || captured.AcceptAgentPackKeysCommand != command || captured.AgentPackKeysNotEvaluated != "" {
		t.Fatalf("awaiting keys were not passed: %+v", captured)
	}
	if !strings.Contains(output.String(), "[WARN] agent-pack-keys-awaiting: Agent Pack keys awaiting acceptance: platform-key-2") || !strings.Contains(output.String(), command) {
		t.Fatalf("output:\n%s", output.String())
	}
}

func TestDoctorConfigReportsAwaitingAgentPackKeysNotEvaluatedWhenTheStateDirectoryCannotBeResolved(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err    error
		reason string
	}{
		"not enrolled": {remoteconfig.ErrNotEnrolled, "the state directory is not enrolled"},
		"unresolved":   {errors.New("gateway unreachable and no cached configuration at /secret/path"), "the runner configuration could not be resolved from the state directory"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Provider = "claude"
			var output bytes.Buffer
			err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml"}, doctorCommandDependencies{
				load: func(string) (config.Config, error) { return cfg, nil },
				loadStateDirAwaiting: func(string) (config.Config, []doctor.AwaitingAgentPackKey, string, error) {
					return config.Config{}, nil, "", tc.err
				},
				run: func(ctx context.Context, opts doctor.Options) doctor.Report {
					if opts.AgentPackKeysNotEvaluated != tc.reason || len(opts.AwaitingAgentPackKeys) != 0 {
						t.Fatalf("options = %+v", opts)
					}
					return doctor.RunWithDependencies(ctx, opts, doctor.Dependencies{
						SystemChecks: func(context.Context, doctor.Options) []doctor.Check { return nil },
						LookPath:     func(string) (string, error) { return os.Args[0], nil },
						Stat:         os.Stat,
					})
				},
				output: &output,
			})
			if err != nil {
				t.Fatalf("unresolved state dir failed the command: %v\n%s", err, output.String())
			}
			if !strings.Contains(output.String(), "[WARN] agent-pack-keys-awaiting: Agent Pack keys awaiting acceptance were not evaluated: "+tc.reason) || strings.Contains(output.String(), "/secret/") {
				t.Fatalf("output:\n%s", output.String())
			}
		})
	}
}
