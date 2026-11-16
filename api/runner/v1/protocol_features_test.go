package v1

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestJobSpecDecodesProviderAuthMode(t *testing.T) {
	var job JobSpec
	if err := json.Unmarshal([]byte(`{"job_id":"job-1","provider_auth_mode":"api_key"}`), &job); err != nil {
		t.Fatal(err)
	}
	if job.ProviderAuthMode != ProviderAuthModeAPIKey {
		t.Fatalf("provider_auth_mode = %q", job.ProviderAuthMode)
	}
	encoded, err := json.Marshal(JobSpec{JobID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "provider_auth_mode") {
		t.Fatalf("empty provider_auth_mode must be omitted: %s", encoded)
	}
}

func TestCapabilityManifestPlacesProtocolFeaturesAfterTheElevatedCapabilityVersion(t *testing.T) {
	encoded, err := json.Marshal(CapabilityManifest{
		ElevatedCapabilityTransportVersion: ElevatedCapabilityTransportVersion,
		ProtocolFeatures:                   SupportedProtocolFeatures(),
		RunnerConfigurationDigest:          strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	elevated := strings.Index(text, `"elevated_capability_transport_version":2`)
	features := strings.Index(text, `"protocol_features":["job_spec_provider_auth_mode","provider_auth_prefer_subscription"]`)
	digest := strings.Index(text, `"runner_configuration_digest"`)
	if elevated < 0 || features < elevated || digest < features {
		t.Fatalf("manifest field order = %s", text)
	}
}

func TestSupportedProtocolFeaturesAreSortedAndCopied(t *testing.T) {
	features := SupportedProtocolFeatures()
	if !reflect.DeepEqual(features, []string{ProtocolFeatureJobSpecProviderAuthMode, ProtocolFeatureProviderAuthPreferSubscription}) {
		t.Fatalf("features = %v", features)
	}
	features[0] = "changed"
	if SupportedProtocolFeatures()[0] != ProtocolFeatureJobSpecProviderAuthMode {
		t.Fatal("SupportedProtocolFeatures returned shared storage")
	}
}

func TestElevatedCapabilityOutcomeReasonsMatchTheContractFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/elevated-capability-outcome-reasons.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Reasons []string `json:"reasons"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.Reasons, ElevatedCapabilityOutcomeReasons) {
		t.Fatalf("fixture reasons %v differ from wire reasons %v", fixture.Reasons, ElevatedCapabilityOutcomeReasons)
	}
}
