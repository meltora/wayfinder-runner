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
	if !reflect.DeepEqual(m.ProtocolFeatures, []string{v1.ProtocolFeatureJobSpecProviderAuthMode}) {
		t.Fatalf("protocol features = %v", m.ProtocolFeatures)
	}
	if m.ElevatedCapabilityTransportVersion != 2 {
		t.Fatalf("elevated capability transport version = %d", m.ElevatedCapabilityTransportVersion)
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"elevated_capability_transport_version":2,"protocol_features":["job_spec_provider_auth_mode"]`) {
		t.Fatalf("manifest JSON = %s", encoded)
	}
}
