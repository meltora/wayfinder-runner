package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewHostingDefaultsAreDisabledWithADRLimits(t *testing.T) {
	cfg := Normalize(Config{})
	hosting := cfg.PreviewHosting
	if hosting.Enabled {
		t.Fatal("preview hosting is enabled by default")
	}
	if hosting.MaxConcurrent != 3 || hosting.LeaseMaxSeconds != 45*60 || hosting.MaxExtensionSeconds != 7*24*60*60 {
		t.Fatalf("defaults = %+v", hosting)
	}
	if hosting.MemoryBudgetMB != 0 {
		t.Fatalf("memory budget default must stay 0 (50%% of host RAM, resolved at startup), got %d", hosting.MemoryBudgetMB)
	}
	if Default().PreviewHosting.MaxConcurrent != 3 {
		t.Fatalf("Default() preview hosting = %+v", Default().PreviewHosting)
	}
}

func TestNormalizeDeepCopiesPreviewHostingPhaseTimeouts(t *testing.T) {
	in := Config{PreviewHosting: PreviewHostingConfig{PhaseTimeoutSeconds: map[string]int{"build": 60}}}
	out := Normalize(in)
	out.PreviewHosting.PhaseTimeoutSeconds["build"] = 1
	if in.PreviewHosting.PhaseTimeoutSeconds["build"] != 60 {
		t.Fatal("normalize aliases the phase timeout map")
	}
}

func TestPreviewHostingAcceptsBoundedConfiguration(t *testing.T) {
	cfg := productionConfig()
	cfg.PreviewHosting = PreviewHostingConfig{
		Enabled: true, MaxConcurrent: 2, MemoryBudgetMB: 4096, LeaseMaxSeconds: 3600,
		PhaseTimeoutSeconds: map[string]int{"install": 900, "build": 900}, MaxExtensionSeconds: 3600,
		TeardownTimeoutSeconds: 60,
	}
	if err := Normalize(cfg).Validate(); err != nil {
		t.Fatalf("valid preview hosting rejected: %v", err)
	}
}

func TestPreviewHostingRejectsUnsafeConfiguration(t *testing.T) {
	tests := map[string]func(*Config){
		"concurrency negative": func(cfg *Config) { cfg.PreviewHosting.MaxConcurrent = -1 },
		"concurrency huge":     func(cfg *Config) { cfg.PreviewHosting.MaxConcurrent = 65 },
		"budget negative":      func(cfg *Config) { cfg.PreviewHosting.MemoryBudgetMB = -1 },
		"lease short":          func(cfg *Config) { cfg.PreviewHosting.LeaseMaxSeconds = 59 },
		"lease long":           func(cfg *Config) { cfg.PreviewHosting.LeaseMaxSeconds = 6*60*60 + 1 },
		"extension negative":   func(cfg *Config) { cfg.PreviewHosting.MaxExtensionSeconds = -1 },
		"extension long":       func(cfg *Config) { cfg.PreviewHosting.MaxExtensionSeconds = 30*24*60*60 + 1 },
		"teardown negative":    func(cfg *Config) { cfg.PreviewHosting.TeardownTimeoutSeconds = -1 },
		"teardown long":        func(cfg *Config) { cfg.PreviewHosting.TeardownTimeoutSeconds = 601 },
		"unknown phase": func(cfg *Config) {
			cfg.PreviewHosting.PhaseTimeoutSeconds = map[string]int{"shell": 10}
		},
		"lease_expired is not a phase timeout": func(cfg *Config) {
			cfg.PreviewHosting.PhaseTimeoutSeconds = map[string]int{"lease_expired": 10}
		},
		"zero phase": func(cfg *Config) {
			cfg.PreviewHosting.PhaseTimeoutSeconds = map[string]int{"build": 0}
		},
		"phase sum exceeds lease": func(cfg *Config) {
			cfg.PreviewHosting.LeaseMaxSeconds = 600
			cfg.PreviewHosting.PhaseTimeoutSeconds = map[string]int{"install": 400, "build": 400}
		},
		"enabled without docker": func(cfg *Config) {
			cfg.PreviewHosting.Enabled = true
			cfg.Isolation = "direct"
			cfg.AllowUnsafeDirect = true
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := productionConfig()
			mutate(&cfg)
			if err := Normalize(cfg).Validate(); err == nil {
				t.Fatal("unsafe preview hosting configuration accepted")
			}
		})
	}
}

func TestLoadReadsPreviewHostingBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := strings.Join([]string{
		"control_plane_url: wss://cp.example.com/stream",
		"job_image: registry.example.com/runner/job@sha256:" + strings.Repeat("a", 64),
		"preview_hosting:",
		"  enabled: true",
		"  max_concurrent: 2",
		"  memory_budget_mb: 2048",
		"  phase_timeout_seconds:",
		"    build: 300",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	hosting := cfg.PreviewHosting
	if !hosting.Enabled || hosting.MaxConcurrent != 2 || hosting.MemoryBudgetMB != 2048 ||
		hosting.PhaseTimeoutSeconds["build"] != 300 || hosting.LeaseMaxSeconds != 45*60 {
		t.Fatalf("loaded preview hosting = %+v", hosting)
	}
}
