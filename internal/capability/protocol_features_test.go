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
	want := []string{v1.ProtocolFeatureJobSpecProviderAuthMode, v1.ProtocolFeatureProviderAuthPreferSubscription}
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
	if !strings.Contains(string(encoded), `"elevated_capability_transport_version":2,"protocol_features":["job_spec_provider_auth_mode","provider_auth_prefer_subscription"]`) {
		t.Fatalf("manifest JSON = %s", encoded)
	}
}
