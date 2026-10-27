package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
)

var resolveDockerUserMapping = isolation.ResolveUserMapping

func dockerDaemonRefusal(info isolation.DockerInfo, goos string) error {
	if info.Available && (goos != "linux" || info.Rootless) {
		return nil
	}
	if info.Available && info.UsernsRemap {
		return fmt.Errorf("userns-remap Docker daemons are not supported yet; production Docker isolation requires a rootless daemon owned by the runner's user")
	}
	return fmt.Errorf("production Docker isolation requires an available rootless Docker daemon")
}

func resolveDockerJobUser(cfg config.Config, runnerID string) (isolation.UserMapping, isolation.Ownership, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mapping, err := resolveDockerUserMapping(ctx, cfg.JobImage, runnerID)
	if err != nil {
		return isolation.UserMapping{}, nil, fmt.Errorf("resolve the Docker job user: %w", err)
	}
	if !mapping.RequiresOwnershipHandOff() {
		return mapping, nil, nil
	}
	stateDir, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return isolation.UserMapping{}, nil, fmt.Errorf("resolve the runner state directory: %w", err)
	}
	return mapping, isolation.DockerOwnership{Mapping: mapping, Image: cfg.JobImage, StateDir: filepath.Clean(stateDir)}, nil
}
