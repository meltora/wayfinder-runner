package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/doctor"
	"github.com/example/app-runner/internal/remoteconfig"
)

type doctorCommandDependencies struct {
	load                 func(string) (config.Config, error)
	loadStateDir         func(string) (config.Config, error)
	loadStateDirAwaiting func(string) (config.Config, []doctor.AwaitingAgentPackKey, string, error)
	run                  func(context.Context, doctor.Options) doctor.Report
	output               io.Writer
}

func cmdDoctorWithDependencies(args []string, deps doctorCommandDependencies) error {
	flags, err := parseFlags(args)
	if err != nil {
		return err
	}
	opts := doctor.Options{ControlPlaneURL: flags.url, StateDir: flags.stateDir}
	if flags.config != "" {
		if flags.url != "" || flags.stateDir != config.Default().StateDir {
			return fmt.Errorf("doctor --config cannot be combined with --url or --state-dir")
		}
		cfg, err := deps.load(flags.config)
		if err != nil {
			return fmt.Errorf("runner configuration could not be loaded or validated")
		}
		opts = loadedConfigurationDoctorOptions(cfg)
		resolveConfiguredAwaitingAgentPackKeys(&opts, cfg.StateDir, deps.loadStateDirAwaiting)
	} else if flags.url == "" && flags.stateDirSet && deps.loadStateDirAwaiting != nil {
		cfg, awaiting, command, err := deps.loadStateDirAwaiting(flags.stateDir)
		if err != nil {
			return err
		}
		opts = loadedConfigurationDoctorOptions(cfg)
		opts.AwaitingAgentPackKeys = awaiting
		opts.AcceptAgentPackKeysCommand = command
	} else if flags.url == "" && flags.stateDirSet && deps.loadStateDir != nil {
		cfg, err := deps.loadStateDir(flags.stateDir)
		if err != nil {
			return err
		}
		opts = loadedConfigurationDoctorOptions(cfg)
	} else if flags.url == "" {
		return fmt.Errorf("doctor requires --state-dir DIR, --config FILE or --url URL")
	}
	report := deps.run(context.Background(), opts)
	if _, err := fmt.Fprint(deps.output, doctor.Format(report)); err != nil {
		return fmt.Errorf("could not write runner diagnostics")
	}
	if report.ExitCode() != 0 {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}

func resolveConfiguredAwaitingAgentPackKeys(
	opts *doctor.Options,
	stateDir string,
	load func(string) (config.Config, []doctor.AwaitingAgentPackKey, string, error),
) {
	if load == nil {
		opts.AgentPackKeysNotEvaluated = "the runner configuration could not be resolved from the state directory"
		return
	}
	_, awaiting, command, err := load(stateDir)
	switch {
	case errors.Is(err, remoteconfig.ErrNotEnrolled):
		opts.AgentPackKeysNotEvaluated = "the state directory is not enrolled"
	case err != nil:
		opts.AgentPackKeysNotEvaluated = "the runner configuration could not be resolved from the state directory"
	default:
		opts.AwaitingAgentPackKeys = awaiting
		opts.AcceptAgentPackKeysCommand = command
	}
}

func loadedConfigurationDoctorOptions(cfg config.Config) doctor.Options {
	return doctor.Options{
		ConfigurationLoaded: true, Provider: cfg.Provider, ProviderBinary: cfg.ProviderBinaries[cfg.Provider],
		RepositoryCount: len(cfg.Repositories), TrustedAgentPackKeyCount: len(cfg.AgentPackPublicKeys),
		ControlPlaneURL: cfg.ControlPlaneURL, StateDir: cfg.StateDir, Isolation: cfg.Isolation,
		JobImage: cfg.JobImage, JobNetwork: cfg.JobNetwork, JobNetworkName: cfg.JobNetworkName,
		EgressPolicyID: cfg.EgressPolicyID, WorkIntakeProposalEnabled: cfg.WorkIntakeProposal.Enabled, EgressPolicyVerifierSocket: cfg.EgressPolicyVerifierSocket,
		ProviderEnvironmentNames: map[string][]string{
			"codex": cfg.ProviderEnvironmentNames("codex"), "claude": cfg.ProviderEnvironmentNames("claude"), "opencode": cfg.ProviderEnvironmentNames("opencode"),
		},
	}
}
