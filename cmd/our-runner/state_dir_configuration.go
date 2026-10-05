package main

import (
	"context"
	"fmt"
	"io"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/doctor"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/remoteconfig"
)

func loadStateDirConfiguration(ctx context.Context, stateDir string, warnings io.Writer) (config.Config, error) {
	cfg, _, err := loadStateDirConfigurationWithReloader(ctx, stateDir, warnings)
	return cfg, err
}

type stateDirConfiguration struct {
	config        config.Config
	reloader      *remoteconfig.Reloader
	awaiting      []doctor.AwaitingAgentPackKey
	acceptCommand string
}

func acceptPackKeysCommand(gatewayURL string, fromStoredFile bool, stateDir string) string {
	return "our-runner configure" + gatewayArgument(gatewayURL, fromStoredFile) + " --state-dir " + shellQuote(stateDir) + " --accept-pack-keys"
}

func loadStateDirConfigurationForDoctor(ctx context.Context, stateDir string, warnings io.Writer) (config.Config, []doctor.AwaitingAgentPackKey, string, error) {
	resolved, err := resolveStateDirConfiguration(ctx, stateDir, warnings)
	if err != nil {
		return config.Config{}, nil, "", err
	}
	return resolved.config, resolved.awaiting, resolved.acceptCommand, nil
}

func loadStateDirConfigurationWithReloader(ctx context.Context, stateDir string, warnings io.Writer) (config.Config, *remoteconfig.Reloader, error) {
	resolved, err := resolveStateDirConfiguration(ctx, stateDir, warnings)
	if err != nil {
		return config.Config{}, nil, err
	}
	for _, key := range resolved.awaiting {
		fmt.Fprintf(warnings, "warning: Agent Pack key %s (sha256:%s) is awaiting host acceptance; run %s\n", key.ID, key.Fingerprint, resolved.acceptCommand)
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
	resolved, err := remoteconfig.Resolve(ctx, stateDir, runnerID, func(gatewayURL string) (remoteconfig.FetchFunc, error) {
		return newRemoteFetch(stateDir, gatewayURL)
	}, remoteconfig.StartPolicy())
	if err != nil {
		return stateDirConfiguration{}, err
	}
	if resolved.FromCache {
		fmt.Fprintf(warnings, "warning: using the cached runner configuration because the gateway could not be reached: %v\n", resolved.FetchErr)
	}
	gatewayURL := resolved.Merged.Config.ControlPlaneURL
	stored, ok, _ := loadStoredGatewayURL(stateDir)
	awaiting := make([]doctor.AwaitingAgentPackKey, 0, len(resolved.Merged.AwaitingPackKeys))
	for _, key := range resolved.Merged.AwaitingPackKeys {
		awaiting = append(awaiting, doctor.AwaitingAgentPackKey{ID: key.ID, Fingerprint: key.Fingerprint})
	}
	cfg := resolved.Merged.Config
	cfg.RunnerConfigurationDigest = resolved.Delivery.Digest
	reloader := remoteconfig.NewReloader(stateDir, runnerID, resolved.Local, resolved.Delivery, func(ctx context.Context, runningDigest string) ([]byte, error) {
		fetcher, err := remoteconfig.NewFetcher(stateDir, gatewayURL)
		if err != nil {
			return nil, err
		}
		return fetcher.FetchIfChanged(ctx, runningDigest)
	})
	return stateDirConfiguration{
		config: cfg, reloader: reloader, awaiting: awaiting,
		acceptCommand: acceptPackKeysCommand(gatewayURL, ok && stored == gatewayURL, stateDir),
	}, nil
}
