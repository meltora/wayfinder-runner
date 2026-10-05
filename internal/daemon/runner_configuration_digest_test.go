package daemon

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
)

func TestPublishCapabilityReportsTheRunningConfigurationDigest(t *testing.T) {
	for _, digest := range []string{strings.Repeat("b", 64), ""} {
		stateDir := t.TempDir()
		events := &capturedEvents{}
		cfg := config.Normalize(config.Config{StateDir: stateDir, ControlPlaneHTTPURL: "https://control.example.test"})
		cfg.RunnerConfigurationDigest = digest
		d := &Daemon{
			cfg:       cfg,
			store:     identity.NewStore(stateDir),
			runnerID:  "runner-1",
			logger:    slog.Default(),
			eventSink: events.sink,
		}
		if _, err := d.store.EnsureKey(); err != nil {
			t.Fatal(err)
		}
		if err := d.publishCapability(context.Background()); err != nil {
			t.Fatal(err)
		}
		published := events.byKind("capability_manifest")
		if len(published) != 1 {
			t.Fatalf("events = %v", events.events)
		}
		manifest, ok := published[0]["manifest"].(v1.CapabilityManifest)
		if !ok || manifest.Signature == "" || manifest.RunnerConfigurationDigest != digest {
			t.Fatalf("manifest = %#v", published[0]["manifest"])
		}
	}
}
