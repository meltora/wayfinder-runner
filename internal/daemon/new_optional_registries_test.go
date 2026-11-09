package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

func newTestPreviewProfile() config.PreviewObservationProfileConfig {
	return config.PreviewObservationProfileConfig{
		BlueprintID: "node-web-app", Version: "2026.08.20", Digest: strings.Repeat("a", 64),
		AllowedHostnameSuffixes: []string{"preview.example.com"}, AllowedPrivateCIDRs: []string{"10.40.0.0/16"},
		MaxResponseBytes: 64 << 10,
		HealthChecks: map[string]config.PreviewObservationHealthCheckConfig{
			"http-smoke": {Path: "/healthz", ExpectedStatus: []int{200, 204}, TimeoutSeconds: 10},
		},
	}
}

func newTestPreviewController() config.PreviewLifecycleControllerConfig {
	return config.PreviewLifecycleControllerConfig{
		BlueprintID: "node-web-app", Version: "2026.08.23", Digest: strings.Repeat("a", 64),
		ControllerURL: "https://preview-controller.example.com/v1/preview-lifecycle",
		Actions:       []string{"destroy", "extend"}, TimeoutSeconds: 20, MaxResponseBytes: 64 << 10,
		MaxExtensionMS: uint64(30 * 24 * 60 * 60 * 1000),
	}
}

func privateToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewWiresEverySourcePublicationRoleFromOneInstallationRegistry(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	cfg.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{
		"github-main": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: privateToken(t, "ghs_test_token_value")},
	}
	d, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if d.sourcePublicationPublisher == nil || d.reviewerIdentityVerifier == nil || d.providerReviewerAssigner == nil ||
		d.providerReviewerAssignmentReconciler == nil || d.providerReviewObserver == nil || d.providerPullRequestRunner == nil {
		t.Fatalf("source publication roles were not wired: %+v", d)
	}
	if d.previewObserver != nil || d.previewLifecycleController != nil {
		t.Fatal("source publication enabled preview capabilities")
	}
}

func TestNewRefusesASourcePublicationInstallationWithAnInvalidCredential(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	cfg.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{
		"gitlab-main": {Provider: "gitlab", TokenFile: privateToken(t, "short")},
	}
	d, err := New(cfg, nil)
	if err == nil {
		closeConstructedDaemon(d)
		t.Fatal("New accepted an invalid source publication credential")
	}
	if !strings.Contains(err.Error(), "initialize source publication credentials") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "journal.db")); !os.IsNotExist(err) {
		t.Fatalf("a refused start opened the journal: %v", err)
	}
}

func TestNewWiresConfiguredPreviewObserverAndLifecycleController(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	cfg.PreviewObservationProfiles = map[string]config.PreviewObservationProfileConfig{"node-preview": newTestPreviewProfile()}
	cfg.PreviewLifecycleControllers = map[string]config.PreviewLifecycleControllerConfig{"node-web": newTestPreviewController()}
	d, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if d.previewObserver == nil || d.previewLifecycleController == nil {
		t.Fatal("preview observer or lifecycle controller was not wired")
	}
	if d.sourcePublicationPublisher != nil || d.providerPullRequestRunner != nil {
		t.Fatal("preview configuration enabled source publication")
	}
}

func TestNewRefusesAmbiguousPreviewCapabilities(t *testing.T) {
	for name, configure := range map[string]func(*config.Config){
		"observation": func(cfg *config.Config) {
			cfg.PreviewObservationProfiles = map[string]config.PreviewObservationProfileConfig{
				"node-preview": newTestPreviewProfile(), "node-preview-copy": newTestPreviewProfile(),
			}
		},
		"lifecycle": func(cfg *config.Config) {
			cfg.PreviewLifecycleControllers = map[string]config.PreviewLifecycleControllerConfig{
				"node-web": newTestPreviewController(), "node-web-copy": newTestPreviewController(),
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := enrolledDirectConfig(t)
			configure(&cfg)
			d, err := New(cfg, nil)
			if err == nil {
				closeConstructedDaemon(d)
				t.Fatal("New accepted two preview entries with one capability identity")
			}
			if !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewRefusesAnInvalidConfigurationBeforeTouchingState(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	cfg.Transport = "carrier-pigeon"
	d, err := New(cfg, nil)
	if err == nil {
		closeConstructedDaemon(d)
		t.Fatal("New accepted an invalid configuration")
	}
	if !strings.Contains(err.Error(), "invalid runner configuration") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "journal.db")); !os.IsNotExist(err) {
		t.Fatalf("an invalid configuration opened the journal: %v", err)
	}
}
