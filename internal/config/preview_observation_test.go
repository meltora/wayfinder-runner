package config

import (
	"strings"
	"testing"
)

func validPreviewObservationProfile() PreviewObservationProfileConfig {
	return PreviewObservationProfileConfig{
		BlueprintID:             "node-web-app",
		Version:                 "2026.08.20",
		Digest:                  strings.Repeat("a", 64),
		AllowedHostnameSuffixes: []string{"preview.example.com"},
		AllowedPrivateCIDRs:     []string{"10.40.0.0/16"},
		MaxResponseBytes:        64 << 10,
		HealthChecks: map[string]PreviewObservationHealthCheckConfig{
			"http-smoke": {
				Path:           "/healthz",
				ExpectedStatus: []int{200, 204},
				TimeoutSeconds: 10,
			},
		},
	}
}

func TestPreviewObservationProfileAcceptsExplicitBoundedPolicy(t *testing.T) {
	cfg := productionConfig()
	cfg.PreviewObservationProfiles = map[string]PreviewObservationProfileConfig{
		"node-preview": validPreviewObservationProfile(),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid preview observation profile rejected: %v", err)
	}
}

func TestPreviewObservationProfileRejectsUnsafePolicy(t *testing.T) {
	tests := map[string]func(*PreviewObservationProfileConfig){
		"profile id": func(profile *PreviewObservationProfileConfig) {},
		"blueprint id": func(profile *PreviewObservationProfileConfig) {
			profile.BlueprintID = "../other"
		},
		"version": func(profile *PreviewObservationProfileConfig) {
			profile.Version = " "
		},
		"digest": func(profile *PreviewObservationProfileConfig) {
			profile.Digest = strings.Repeat("A", 64)
		},
		"uppercase suffix": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedHostnameSuffixes = []string{"Preview.Example.com"}
		},
		"leading dot suffix": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedHostnameSuffixes = []string{".preview.example.com"}
		},
		"unqualified suffix": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedHostnameSuffixes = []string{"internal"}
		},
		"empty hostname label": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedHostnameSuffixes = []string{"preview..example.com"}
		},
		"hostname label hyphen": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedHostnameSuffixes = []string{"-preview.example.com"}
		},
		"duplicate suffix": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedHostnameSuffixes = []string{"preview.example.com", "preview.example.com"}
		},
		"public cidr": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedPrivateCIDRs = []string{"203.0.113.0/24"}
		},
		"loopback cidr": func(profile *PreviewObservationProfileConfig) {
			profile.AllowedPrivateCIDRs = []string{"127.0.0.0/8"}
		},
		"relative ca": func(profile *PreviewObservationProfileConfig) {
			profile.TrustedCAFile = "ca.pem"
		},
		"relative authorization": func(profile *PreviewObservationProfileConfig) {
			profile.AuthorizationFile = "token"
		},
		"response limit": func(profile *PreviewObservationProfileConfig) {
			profile.MaxResponseBytes = 2 << 20
		},
		"check id": func(profile *PreviewObservationProfileConfig) {
			profile.HealthChecks = map[string]PreviewObservationHealthCheckConfig{"../health": {Path: "/healthz"}}
		},
		"relative path": func(profile *PreviewObservationProfileConfig) {
			check := profile.HealthChecks["http-smoke"]
			check.Path = "healthz"
			profile.HealthChecks["http-smoke"] = check
		},
		"query path": func(profile *PreviewObservationProfileConfig) {
			check := profile.HealthChecks["http-smoke"]
			check.Path = "/healthz?verbose=true"
			profile.HealthChecks["http-smoke"] = check
		},
		"duplicate status": func(profile *PreviewObservationProfileConfig) {
			check := profile.HealthChecks["http-smoke"]
			check.ExpectedStatus = []int{200, 200}
			profile.HealthChecks["http-smoke"] = check
		},
		"invalid status": func(profile *PreviewObservationProfileConfig) {
			check := profile.HealthChecks["http-smoke"]
			check.ExpectedStatus = []int{99}
			profile.HealthChecks["http-smoke"] = check
		},
		"timeout": func(profile *PreviewObservationProfileConfig) {
			check := profile.HealthChecks["http-smoke"]
			check.TimeoutSeconds = 121
			profile.HealthChecks["http-smoke"] = check
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			profile := validPreviewObservationProfile()
			cfg := productionConfig()
			profileID := "node-preview"
			if name == "profile id" {
				profileID = "../preview"
			} else {
				mutate(&profile)
			}
			cfg.PreviewObservationProfiles = map[string]PreviewObservationProfileConfig{profileID: profile}
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe preview observation profile accepted")
			}
		})
	}
}

func TestNormalizeDeepCopiesPreviewObservationProfiles(t *testing.T) {
	profile := validPreviewObservationProfile()
	in := Config{PreviewObservationProfiles: map[string]PreviewObservationProfileConfig{"node-preview": profile}}
	out := Normalize(in)
	copyProfile := out.PreviewObservationProfiles["node-preview"]
	copyProfile.AllowedHostnameSuffixes[0] = "changed.example.com"
	copyProfile.AllowedPrivateCIDRs[0] = "10.99.0.0/16"
	copyCheck := copyProfile.HealthChecks["http-smoke"]
	copyCheck.ExpectedStatus[0] = 500

	original := in.PreviewObservationProfiles["node-preview"]
	if original.AllowedHostnameSuffixes[0] != "preview.example.com" ||
		original.AllowedPrivateCIDRs[0] != "10.40.0.0/16" ||
		original.HealthChecks["http-smoke"].ExpectedStatus[0] != 200 {
		t.Fatal("Normalize mutated the caller's preview observation profile")
	}
}
