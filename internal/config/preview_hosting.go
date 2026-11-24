package config

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	DefaultPreviewHostingMaxConcurrent       = 3
	DefaultPreviewHostingLeaseMaxSeconds     = 45 * 60
	DefaultPreviewHostingMaxExtensionSeconds = 7 * 24 * 60 * 60
	maxPreviewHostingConcurrent              = 64
	minPreviewHostingLeaseSeconds            = 60
	maxPreviewHostingLeaseSeconds            = 6 * 60 * 60
	maxPreviewHostingExtensionSeconds        = 30 * 24 * 60 * 60
	maxPreviewHostingTeardownSeconds         = 600
)

var previewHostingPhases = map[string]bool{
	string(v1.PreviewDeployPhaseCheckout): true,
	string(v1.PreviewDeployPhasePull):     true,
	string(v1.PreviewDeployPhaseServices): true,
	string(v1.PreviewDeployPhaseInstall):  true,
	string(v1.PreviewDeployPhaseBuild):    true,
	string(v1.PreviewDeployPhaseMigrate):  true,
	string(v1.PreviewDeployPhaseSeed):     true,
	string(v1.PreviewDeployPhaseStart):    true,
	string(v1.PreviewDeployPhaseHealth):   true,
}

type PreviewHostingConfig struct {
	Enabled                bool           `yaml:"enabled,omitempty"`
	MaxConcurrent          int            `yaml:"max_concurrent,omitempty"`
	MemoryBudgetMB         int            `yaml:"memory_budget_mb,omitempty"`
	LeaseMaxSeconds        int            `yaml:"lease_max_seconds,omitempty"`
	PhaseTimeoutSeconds    map[string]int `yaml:"phase_timeout_seconds,omitempty"`
	MaxExtensionSeconds    int            `yaml:"max_extension_seconds,omitempty"`
	TeardownTimeoutSeconds int            `yaml:"teardown_timeout_seconds,omitempty"`
}

func defaultPreviewHosting() PreviewHostingConfig {
	return PreviewHostingConfig{
		MaxConcurrent:       DefaultPreviewHostingMaxConcurrent,
		LeaseMaxSeconds:     DefaultPreviewHostingLeaseMaxSeconds,
		MaxExtensionSeconds: DefaultPreviewHostingMaxExtensionSeconds,
	}
}

func normalizePreviewHosting(in PreviewHostingConfig) PreviewHostingConfig {
	out := in
	defaults := defaultPreviewHosting()
	if out.MaxConcurrent == 0 {
		out.MaxConcurrent = defaults.MaxConcurrent
	}
	if out.LeaseMaxSeconds == 0 {
		out.LeaseMaxSeconds = defaults.LeaseMaxSeconds
	}
	if out.MaxExtensionSeconds == 0 {
		out.MaxExtensionSeconds = defaults.MaxExtensionSeconds
	}
	if in.PhaseTimeoutSeconds != nil {
		out.PhaseTimeoutSeconds = make(map[string]int, len(in.PhaseTimeoutSeconds))
		for phase, seconds := range in.PhaseTimeoutSeconds {
			out.PhaseTimeoutSeconds[phase] = seconds
		}
	}
	return out
}

func validatePreviewHosting(hosting PreviewHostingConfig, isolation string) error {
	if hosting.Enabled && isolation != "docker" {
		return fmt.Errorf("preview_hosting requires docker isolation")
	}
	if hosting.MaxConcurrent < 0 || hosting.MaxConcurrent > maxPreviewHostingConcurrent {
		return fmt.Errorf("preview_hosting.max_concurrent must be between 1 and %d", maxPreviewHostingConcurrent)
	}
	if hosting.MemoryBudgetMB < 0 {
		return fmt.Errorf("preview_hosting.memory_budget_mb must not be negative")
	}
	lease := hosting.LeaseMaxSeconds
	if lease == 0 {
		lease = DefaultPreviewHostingLeaseMaxSeconds
	}
	if lease < minPreviewHostingLeaseSeconds || lease > maxPreviewHostingLeaseSeconds {
		return fmt.Errorf("preview_hosting.lease_max_seconds must be between %d and %d", minPreviewHostingLeaseSeconds, maxPreviewHostingLeaseSeconds)
	}
	if hosting.MaxExtensionSeconds < 0 || hosting.MaxExtensionSeconds > maxPreviewHostingExtensionSeconds {
		return fmt.Errorf("preview_hosting.max_extension_seconds must be between 1 and %d", maxPreviewHostingExtensionSeconds)
	}
	if hosting.TeardownTimeoutSeconds < 0 || hosting.TeardownTimeoutSeconds > maxPreviewHostingTeardownSeconds {
		return fmt.Errorf("preview_hosting.teardown_timeout_seconds must be between 1 and %d", maxPreviewHostingTeardownSeconds)
	}
	sum := 0
	for phase, seconds := range hosting.PhaseTimeoutSeconds {
		if !previewHostingPhases[phase] {
			return fmt.Errorf("preview_hosting.phase_timeout_seconds has unknown phase %q", phase)
		}
		if seconds <= 0 || seconds > lease {
			return fmt.Errorf("preview_hosting.phase_timeout_seconds.%s must be between 1 and the lease", phase)
		}
		sum += seconds
	}
	if sum > lease {
		return fmt.Errorf("preview_hosting phase timeouts total %ds and exceed lease_max_seconds %ds", sum, lease)
	}
	return nil
}
