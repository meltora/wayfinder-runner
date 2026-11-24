package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
)

func publishPreviewHostingManifest(t *testing.T, controller *fakePreviewHostController) v1.CapabilityManifest {
	t.Helper()
	stateDir := t.TempDir()
	events := &capturedEvents{}
	d := &Daemon{
		cfg:       config.Normalize(config.Config{StateDir: stateDir, ControlPlaneHTTPURL: "https://control.example.test", Labels: map[string]string{"zone": "a"}}),
		store:     identity.NewStore(stateDir),
		runnerID:  "runner-1",
		logger:    slog.Default(),
		eventSink: events.sink,
	}
	if controller != nil {
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
	if !ok {
		t.Fatalf("manifest = %#v", published[0]["manifest"])
	}
	key, err := identity.NewStore(stateDir).LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := manifest
	unsigned.Signature = ""
	raw, err := json.Marshal(&unsigned)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], signature) {
		t.Fatal("capability signature does not verify against the runner identity")
	}
	return manifest
}

func TestPublishCapabilityAdvertisesPreviewHostingFromTheController(t *testing.T) {
	controller := &fakePreviewHostController{}
	manifest := publishPreviewHostingManifest(t, controller)
	want := controller.Capability()
	if manifest.PreviewHostingTransportVersion != v1.PreviewHostingTransportVersion || manifest.PreviewHosting == nil ||
		!reflect.DeepEqual(*manifest.PreviewHosting, want) {
		t.Fatalf("manifest preview hosting = %#v", manifest.PreviewHosting)
	}
}

func TestPublishCapabilityOmitsPreviewHostingWhenDisabled(t *testing.T) {
	manifest := publishPreviewHostingManifest(t, nil)
	if manifest.PreviewHostingTransportVersion != 0 || manifest.PreviewHosting != nil {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestPublishCapabilityDropsAnInvalidPreviewHostingCapability(t *testing.T) {
	zero := uint64(0)
	tooLong := maxPreviewHostingExtensionMS + 1
	atCap := maxPreviewHostingExtensionMS
	valid := (&fakePreviewHostController{}).Capability()
	with := func(change func(*v1.PreviewHostingCapability)) v1.PreviewHostingCapability {
		capability := valid
		capability.Actions = append([]v1.PreviewLifecycleAction(nil), valid.Actions...)
		change(&capability)
		return capability
	}
	cases := map[string]struct {
		capability v1.PreviewHostingCapability
		kept       bool
	}{
		"wrong protocol version":     {with(func(c *v1.PreviewHostingCapability) { c.ProtocolVersion = 2 }), false},
		"zero max concurrent":        {with(func(c *v1.PreviewHostingCapability) { c.MaxConcurrent = 0 }), false},
		"max concurrent above cap":   {with(func(c *v1.PreviewHostingCapability) { c.MaxConcurrent = config.MaxPreviewHostingConcurrent + 1 }), false},
		"no actions":                 {with(func(c *v1.PreviewHostingCapability) { c.Actions = nil }), false},
		"empty actions":              {with(func(c *v1.PreviewHostingCapability) { c.Actions = []v1.PreviewLifecycleAction{} }), false},
		"duplicate action":           {with(func(c *v1.PreviewHostingCapability) { c.Actions = []v1.PreviewLifecycleAction{"deploy", "deploy"} }), false},
		"unknown action":             {with(func(c *v1.PreviewHostingCapability) { c.Actions = []v1.PreviewLifecycleAction{"deploy", "restart"} }), false},
		"zero max extension":         {with(func(c *v1.PreviewHostingCapability) { c.MaxExtensionMS = &zero }), false},
		"max extension above cap":    {with(func(c *v1.PreviewHostingCapability) { c.MaxExtensionMS = &tooLong }), false},
		"max concurrent at cap":      {with(func(c *v1.PreviewHostingCapability) { c.MaxConcurrent = config.MaxPreviewHostingConcurrent }), true},
		"max extension at cap":       {with(func(c *v1.PreviewHostingCapability) { c.MaxExtensionMS = &atCap }), true},
		"no max extension":           {with(func(c *v1.PreviewHostingCapability) { c.MaxExtensionMS = nil }), true},
		"single observe action only": {with(func(c *v1.PreviewHostingCapability) { c.Actions = []v1.PreviewLifecycleAction{"observe"} }), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			capability := tc.capability
			manifest := publishPreviewHostingManifest(t, &fakePreviewHostController{capability: &capability})
			if manifest.RunnerID != "runner-1" || manifest.Labels["zone"] != "a" || manifest.ProtocolVersion != v1.ProtocolVersion {
				t.Fatalf("rest of manifest lost: %#v", manifest)
			}
			if !tc.kept {
				if manifest.PreviewHostingTransportVersion != 0 || manifest.PreviewHosting != nil {
					t.Fatalf("invalid preview hosting kept: %#v", manifest.PreviewHosting)
				}
				return
			}
			if manifest.PreviewHostingTransportVersion != v1.PreviewHostingTransportVersion || manifest.PreviewHosting == nil ||
				!reflect.DeepEqual(*manifest.PreviewHosting, capability) {
				t.Fatalf("valid preview hosting dropped: %#v", manifest.PreviewHosting)
			}
		})
	}
}
