package main

import (
	"context"
	"fmt"
	"io"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/doctor"
)

type doctorCommandDependencies struct {
	load         func(string) (config.Config, error)
	loadStateDir func(string) (config.Config, error)
	run          func(context.Context, doctor.Options) doctor.Report
	output       io.Writer
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

func declaredCheckImages(checks map[string]config.CheckConfig) map[string]string {
	images := map[string]string{}
	for id, check := range checks {
		if check.Image != "" {
			images[id] = check.Image
		}
	}
	return images
}

func loadedConfigurationDoctorOptions(cfg config.Config) doctor.Options {
	return doctor.Options{
		ConfigurationLoaded: true, Provider: cfg.Provider, ProviderBinary: cfg.ProviderBinaries[cfg.Provider],
		PlatformTrustedKeySet: cfg.AgentPackTrustedKeySet, BuildChannel: daemon.Channel,
		RepositoryCount: len(cfg.Repositories), TrustedAgentPackKeyCount: len(cfg.AgentPackPublicKeys),
		ControlPlaneURL: cfg.ControlPlaneURL, StateDir: cfg.StateDir, Isolation: cfg.Isolation,
		JobImage: cfg.JobImage, CheckImages: declaredCheckImages(cfg.Checks), JobNetwork: cfg.JobNetwork, JobNetworkName: cfg.JobNetworkName,
		EgressPolicyID: cfg.EgressPolicyID, WorkIntakeProposalEnabled: cfg.WorkIntakeProposal.Enabled, EgressPolicyVerifierSocket: cfg.EgressPolicyVerifierSocket,
		PreviewHostingEnabled: cfg.PreviewHosting.Enabled, PreviewHostingMaxConcurrent: cfg.PreviewHosting.MaxConcurrent,
		WorkIntakeProposalAdapter: cfg.WorkIntakeProposal.Adapter, WorkIntakeProposalBinary: cfg.ProviderBinaries[cfg.WorkIntakeProposal.Adapter],
		ProviderEnvironmentNames: map[string][]string{
			"codex": cfg.ProviderEnvironmentNames("codex"), "claude": cfg.ProviderEnvironmentNames("claude"), "opencode": cfg.ProviderEnvironmentNames("opencode"),
		},
	}
}
