package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/provider"
)

var probeProviderCLI = isolation.ProbeProviderCLI

type providerCLIKey struct {
	provider string
	image    string
}

var providerReleaseNames = map[string]string{"claude": "Claude Code"}

func dockerProviderNames(cfg config.Config) []string {
	names := []string{cfg.Provider}
	if cfg.WorkIntakeProposal.Enabled && cfg.WorkIntakeProposal.Adapter != cfg.Provider {
		names = append(names, cfg.WorkIntakeProposal.Adapter)
	}
	return names
}

func verifyDockerProviderCLIs(
	cfg config.Config,
	runnerID string,
	mapping isolation.UserMapping,
	logger *slog.Logger,
) (map[providerCLIKey]string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	verified := map[providerCLIKey]string{}
	for _, name := range dockerProviderNames(cfg) {
		adapter, err := provider.For(name)
		if err != nil {
			return nil, err
		}
		contract, ok := adapter.(provider.CLIOptionContract)
		if !ok {
			logger.Debug("provider CLI options not probed; no runner option contract is declared", "provider", name)
			continue
		}
		binary := cfg.ProviderBinaries[name]
		if binary == "" {
			binary = name
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		result, err := probeProviderCLI(ctx, cfg.JobImage, binary, runnerID, mapping)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("verify the %s CLI in the job image %s: %w", name, cfg.JobImage, err)
		}
		if missing := provider.MissingCLIOptions(result.Help, contract.RequiredCLIOptions()); len(missing) > 0 {
			release := providerReleaseNames[name]
			if release == "" {
				release = name
			}
			return nil, fmt.Errorf(
				"the job image %s runs %s %s, which does not accept the runner options %s; rebuild the job image (packaging/job-image) with a %s release that supports them",
				cfg.JobImage, name, result.Version, strings.Join(missing, ", "), release,
			)
		}
		verified[providerCLIKey{provider: name, image: cfg.JobImage}] = result.Version
		logger.Info("job image provider CLI accepts the runner options", "provider", name, "version", result.Version)
	}
	return verified, nil
}

func (d *Daemon) providerCLIVerified(adapter provider.Adapter) bool {
	if _, ok := adapter.(provider.CLIOptionContract); !ok {
		return true
	}
	_, ok := d.verifiedProviderCLIs[providerCLIKey{provider: adapter.Name(), image: d.cfg.JobImage}]
	return ok
}
