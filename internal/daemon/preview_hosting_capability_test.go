package daemon

import (
	"context"
	"log/slog"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
)

func TestPublishCapabilityAdvertisesPreviewHostingOnlyWithAController(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		stateDir := t.TempDir()
		events := &capturedEvents{}
		d := &Daemon{
			cfg:       config.Normalize(config.Config{StateDir: stateDir, ControlPlaneHTTPURL: "https://control.example.test"}),
			store:     identity.NewStore(stateDir),
			runnerID:  "runner-1",
			logger:    slog.Default(),
			eventSink: events.sink,
		}
		controller := &fakePreviewHostController{}
		if enabled {
			d.previewHostController = controller
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
		if !ok || manifest.Signature == "" {
			t.Fatalf("manifest = %#v", published[0]["manifest"])
		}
		if !enabled {
			if manifest.PreviewHostingTransportVersion != 0 || manifest.PreviewHosting != nil {
				t.Fatalf("disabled manifest = %#v", manifest)
			}
			continue
		}
		want := controller.Capability()
		if manifest.PreviewHostingTransportVersion != v1.PreviewHostingTransportVersion || manifest.PreviewHosting == nil ||
			manifest.PreviewHosting.ProtocolVersion != want.ProtocolVersion || manifest.PreviewHosting.MaxConcurrent != want.MaxConcurrent ||
			len(manifest.PreviewHosting.Actions) != len(want.Actions) || manifest.PreviewHosting.MaxExtensionMS != nil {
			t.Fatalf("enabled manifest = %#v", manifest)
		}
	}
}
