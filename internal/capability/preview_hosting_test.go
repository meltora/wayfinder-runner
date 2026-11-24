package capability

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestCollectAdvertisesAnImmutablePreviewHostingCapability(t *testing.T) {
	detector := New("runner-1", "1.0.0", nil, nil)
	detector.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	maxExtension := uint64(86_400_000)
	detector.PreviewHosting = &v1.PreviewHostingCapability{
		ProtocolVersion: v1.PreviewHostingProtocolVersion,
		MaxConcurrent:   2,
		Actions:         []v1.PreviewLifecycleAction{"deploy", v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy, "observe"},
		MaxExtensionMS:  &maxExtension,
	}
	manifest := detector.Collect(context.Background())
	if manifest.PreviewHostingTransportVersion != v1.PreviewHostingTransportVersion || manifest.PreviewHosting == nil ||
		manifest.PreviewHosting.MaxConcurrent != 2 || len(manifest.PreviewHosting.Actions) != 4 ||
		manifest.PreviewHosting.MaxExtensionMS == nil || *manifest.PreviewHosting.MaxExtensionMS != maxExtension {
		t.Fatalf("manifest = %+v", manifest)
	}
	manifest.PreviewHosting.Actions[0] = "changed"
	*manifest.PreviewHosting.MaxExtensionMS = 1
	manifest.PreviewHosting.MaxConcurrent = 9
	if detector.PreviewHosting.Actions[0] != "deploy" || *detector.PreviewHosting.MaxExtensionMS != maxExtension || detector.PreviewHosting.MaxConcurrent != 2 {
		t.Fatal("capability collection shared mutable preview hosting state")
	}
}

func TestCollectOmitsPreviewHostingWhenDisabled(t *testing.T) {
	detector := New("runner-1", "1.0.0", nil, nil)
	detector.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	manifest := detector.Collect(context.Background())
	if manifest.PreviewHostingTransportVersion != 0 || manifest.PreviewHosting != nil {
		t.Fatalf("manifest = %+v", manifest)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"preview_hosting`) {
		t.Fatalf("encoded = %s", encoded)
	}
}

func TestPreviewHostingFieldsFollowPreviewLifecycleInTheSignedEncoding(t *testing.T) {
	maxExtension := uint64(3_600_000)
	for _, extension := range []*uint64{&maxExtension, nil} {
		manifest := v1.CapabilityManifest{
			PreviewLifecycleTransportVersion: 1,
			PreviewLifecycleProfiles:         []v1.PreviewLifecycleProfileCapability{{BlueprintID: "a"}},
			PreviewHostingTransportVersion:   1,
			PreviewHosting: &v1.PreviewHostingCapability{
				ProtocolVersion: 1, MaxConcurrent: 3,
				Actions:        []v1.PreviewLifecycleAction{"deploy", v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy, "observe"},
				MaxExtensionMS: extension,
			},
			ReviewerIdentityVerificationTransportVersion: 1,
		}
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		text := string(encoded)
		hosting := `"preview_hosting":{"protocol_version":1,"max_concurrent":3,"actions":["deploy","extend","destroy","observe"]`
		if extension != nil {
			hosting += `,"max_extension_ms":3600000}`
		} else {
			hosting += `}`
		}
		lifecycle := strings.Index(text, `"preview_lifecycle_profiles":`)
		transport := strings.Index(text, `"preview_hosting_transport_version":1,`+hosting+`,"reviewer_identity_verification_transport_version":1`)
		if lifecycle < 0 || transport < lifecycle {
			t.Fatalf("encoded = %s", text)
		}
	}
}
