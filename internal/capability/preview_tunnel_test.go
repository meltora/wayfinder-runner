package capability

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func previewTunnelDetector(hosting, tunnel bool) *Detector {
	detector := New("runner-1", "1.0.0", nil, nil)
	detector.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	if hosting {
		detector.PreviewHosting = &v1.PreviewHostingCapability{
			ProtocolVersion: v1.PreviewHostingProtocolVersion, MaxConcurrent: 2,
			Actions: []v1.PreviewLifecycleAction{"deploy", v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy, "observe"},
		}
	}
	detector.PreviewTunnel = tunnel
	return detector
}

func TestCollectAdvertisesThePreviewTunnelOnlyWithPreviewHosting(t *testing.T) {
	cases := map[string]struct {
		hosting, tunnel bool
		want            int
	}{
		"hosting and tunnel":     {true, true, v1.PreviewTunnelTransportVersion},
		"hosting without tunnel": {true, false, 0},
		"tunnel without hosting": {false, true, 0},
		"neither":                {false, false, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := previewTunnelDetector(tc.hosting, tc.tunnel).Collect(context.Background())
			if manifest.PreviewTunnelTransportVersion != tc.want {
				t.Fatalf("preview tunnel transport version = %d, want %d", manifest.PreviewTunnelTransportVersion, tc.want)
			}
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if advertised := strings.Contains(string(encoded), `"preview_tunnel`); advertised != (tc.want != 0) {
				t.Fatalf("encoded = %s", encoded)
			}
		})
	}
}

func TestPreviewTunnelFieldFollowsPreviewHostingInTheSignedEncoding(t *testing.T) {
	hosting := &v1.PreviewHostingCapability{
		ProtocolVersion: 1, MaxConcurrent: 3,
		Actions: []v1.PreviewLifecycleAction{"deploy", v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy, "observe"},
	}
	base := v1.CapabilityManifest{
		PreviewHostingTransportVersion:               1,
		PreviewHosting:                               hosting,
		ReviewerIdentityVerificationTransportVersion: 1,
	}
	withTunnel := base
	withTunnel.PreviewTunnelTransportVersion = 1
	off, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	on, err := json.Marshal(withTunnel)
	if err != nil {
		t.Fatal(err)
	}
	hostingJSON := `"preview_hosting":{"protocol_version":1,"max_concurrent":3,"actions":["deploy","extend","destroy","observe"]}`
	if !strings.Contains(string(off), hostingJSON+`,"reviewer_identity_verification_transport_version":1`) {
		t.Fatalf("tunnel off = %s", off)
	}
	if !strings.Contains(string(on), hostingJSON+`,"preview_tunnel_transport_version":1,"reviewer_identity_verification_transport_version":1`) {
		t.Fatalf("tunnel on = %s", on)
	}
	if strings.Replace(string(on), `,"preview_tunnel_transport_version":1`, "", 1) != string(off) {
		t.Fatalf("enabling the tunnel changed more than its own field:\n%s\n%s", off, on)
	}
}
