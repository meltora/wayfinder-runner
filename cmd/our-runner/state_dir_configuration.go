package main

import (
	"context"
	"fmt"
	"io"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/remoteconfig"
)

func loadStateDirConfiguration(ctx context.Context, stateDir string, warnings io.Writer) (config.Config, error) {
	cfg, _, err := loadStateDirConfigurationWithReloader(ctx, stateDir, warnings)
	return cfg, err
}

type stateDirConfiguration struct {
	config   config.Config
	reloader *remoteconfig.Reloader
}

func loadStateDirConfigurationWithReloader(ctx context.Context, stateDir string, warnings io.Writer) (config.Config, *remoteconfig.Reloader, error) {
	resolved, err := resolveStateDirConfiguration(ctx, stateDir, warnings)
	if err != nil {
		return config.Config{}, nil, err
	}
	return resolved.config, resolved.reloader, nil
}

func resolveStateDirConfiguration(ctx context.Context, stateDir string, warnings io.Writer) (stateDirConfiguration, error) {
	if !identity.NewStore(stateDir).Enrolled() {
		return stateDirConfiguration{}, remoteconfig.ErrNotEnrolled
	}
	runnerID, err := daemon.ReadRunnerID(stateDir)
	if err != nil {
		return stateDirConfiguration{}, remoteconfig.ErrNotEnrolled
	}
	warnAboutIgnoredConfigFile(warnings, stateDir)
	resolved, err := remoteconfig.Resolve(ctx, stateDir, runnerID, func(gatewayURL string) (remoteconfig.FetchFunc, error) {
		return newRemoteFetch(stateDir, gatewayURL)
	}, remoteconfig.StartPolicy())
	if err != nil {
		return stateDirConfiguration{}, err
	}
	if resolved.FromCache {
		fmt.Fprintf(warnings, "warning: using the cached runner configuration because the gateway could not be reached: %v\n", resolved.FetchErr)
	}
	warnAboutOmittedLocalChecks(warnings, resolved.Merged)
	gatewayURL := resolved.Merged.Config.ControlPlaneURL
	cfg := resolved.Merged.Config
	cfg.RunnerConfigurationDigest = resolved.Delivery.Digest
	reloader := remoteconfig.NewReloader(stateDir, runnerID, resolved.Local, resolved.Delivery, func(ctx context.Context, runningDigest string) ([]byte, error) {
		fetcher, err := remoteconfig.NewFetcher(stateDir, gatewayURL)
		if err != nil {
			return nil, err
		}
		return fetcher.FetchIfChanged(ctx, runningDigest)
	})
	return stateDirConfiguration{config: cfg, reloader: reloader}, nil
}
