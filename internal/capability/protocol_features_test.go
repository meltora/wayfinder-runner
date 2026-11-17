package capability

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestCollectAdvertisesProtocolFeaturesAndTheElevatedCapabilityTransport(t *testing.T) {
	m := fakeDetector().Collect(context.Background())
	want := []string{v1.ProtocolFeatureAgentConfigContentCapture, v1.ProtocolFeatureJobSpecProviderAuthMode, v1.ProtocolFeatureJobSpecWorkAttachments, v1.ProtocolFeatureProviderAuthPreferSubscription, v1.ProtocolFeatureRunNetworkEgressRules}
	if !reflect.DeepEqual(m.ProtocolFeatures, want) {
		t.Fatalf("protocol features = %v", m.ProtocolFeatures)
	}
	if m.ElevatedCapabilityTransportVersion != 2 {
		t.Fatalf("elevated capability transport version = %d", m.ElevatedCapabilityTransportVersion)
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"elevated_capability_transport_version":2,"protocol_features":["agent_config_content_capture_v1","job_spec_provider_auth_mode","job_spec_work_attachments","provider_auth_prefer_subscription","run_network_egress_rules"]`) {
		t.Fatalf("manifest JSON = %s", encoded)
	}
}
