package previewhost

import (
	"fmt"
	"maps"
	"path/filepath"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const (
	DefaultLease           = 45 * time.Minute
	DefaultMaxConcurrent   = 3
	DefaultMaxExtension    = 7 * 24 * time.Hour
	DefaultTeardownTimeout = 90 * time.Second
	DefaultHealthInterval  = time.Second
	DefaultJobUID          = 10001
	DefaultJobGID          = 10001
	logGrace               = 10 * time.Second
)

var deployPhases = []v1.PreviewDeployPhase{
	v1.PreviewDeployPhaseCheckout, v1.PreviewDeployPhasePull, v1.PreviewDeployPhaseServices,
	v1.PreviewDeployPhaseInstall, v1.PreviewDeployPhaseBuild, v1.PreviewDeployPhaseMigrate,
	v1.PreviewDeployPhaseSeed, v1.PreviewDeployPhaseStart, v1.PreviewDeployPhaseHealth,
}

func DefaultPhaseTimeouts() map[v1.PreviewDeployPhase]time.Duration {
	return map[v1.PreviewDeployPhase]time.Duration{
		v1.PreviewDeployPhaseCheckout: 3 * time.Minute,
		v1.PreviewDeployPhasePull:     5 * time.Minute,
		v1.PreviewDeployPhaseServices: 3 * time.Minute,
		v1.PreviewDeployPhaseInstall:  10 * time.Minute,
		v1.PreviewDeployPhaseBuild:    10 * time.Minute,
		v1.PreviewDeployPhaseMigrate:  3 * time.Minute,
		v1.PreviewDeployPhaseSeed:     3 * time.Minute,
		v1.PreviewDeployPhaseStart:    time.Minute,
		v1.PreviewDeployPhaseHealth:   3 * time.Minute,
	}
}

type Config struct {
	RunnerID        string
	StateDir        string
	Lease           time.Duration
	PhaseTimeouts   map[v1.PreviewDeployPhase]time.Duration
	TeardownTimeout time.Duration
	HealthInterval  time.Duration
	MaxConcurrent   int
	MaxExtension    time.Duration
	JobUID          int
	JobGID          int
}

func (cfg Config) normalized() (Config, error) {
	if err := validation.ID("runner_id", cfg.RunnerID); err != nil {
		return Config{}, err
	}
	if !filepath.IsAbs(cfg.StateDir) {
		return Config{}, fmt.Errorf("preview host state directory must be absolute")
	}
	cfg.StateDir = filepath.Clean(cfg.StateDir)
	if cfg.Lease == 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.TeardownTimeout == 0 {
		cfg.TeardownTimeout = DefaultTeardownTimeout
	}
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = DefaultHealthInterval
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}
	if cfg.MaxExtension == 0 {
		cfg.MaxExtension = DefaultMaxExtension
	}
	if cfg.JobUID == 0 {
		cfg.JobUID = DefaultJobUID
	}
	if cfg.JobGID == 0 {
		cfg.JobGID = DefaultJobGID
	}
	if cfg.Lease < 0 || cfg.TeardownTimeout < 0 || cfg.HealthInterval < 0 || cfg.MaxConcurrent < 0 ||
		cfg.MaxExtension < 0 || cfg.JobUID < 0 || cfg.JobGID < 0 {
		return Config{}, fmt.Errorf("preview host configuration has a negative value")
	}
	timeouts := DefaultPhaseTimeouts()
	for phase, timeout := range cfg.PhaseTimeouts {
		if _, known := timeouts[phase]; !known {
			return Config{}, fmt.Errorf("preview host phase %q has no timeout", phase)
		}
		if timeout <= 0 {
			return Config{}, fmt.Errorf("preview host phase %q timeout must be positive", phase)
		}
		timeouts[phase] = timeout
	}
	var sum time.Duration
	for _, timeout := range timeouts {
		sum += timeout
	}
	if budget := sum + 2*cfg.TeardownTimeout + logGrace; budget > cfg.Lease {
		return Config{}, fmt.Errorf("preview host phase timeouts, teardown and log grace %s exceed the deploy lease %s", budget, cfg.Lease)
	}
	cfg.PhaseTimeouts = maps.Clone(timeouts)
	return cfg, nil
}
