package capability

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestCollectAdvertisesExactImmutablePreviewLifecycleProfiles(t *testing.T) {
	detector := New("runner-1", "1.0.0", nil, nil)
	detector.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	maxExtension := uint64(86_400_000)
	detector.PreviewLifecycleProfiles = []v1.PreviewLifecycleProfileCapability{
		{BlueprintID: "z", Version: "1", Digest: strings.Repeat("b", 64), ControllerProtocolVersion: 1, Actions: []v1.PreviewLifecycleAction{v1.PreviewLifecycleDestroy}},
		{BlueprintID: "a", Version: "1", Digest: strings.Repeat("a", 64), ControllerProtocolVersion: 1, Actions: []v1.PreviewLifecycleAction{v1.PreviewLifecycleDestroy, v1.PreviewLifecycleExtend}, MaxExtensionMS: &maxExtension},
	}
	manifest := detector.Collect(context.Background())
	if manifest.PreviewLifecycleTransportVersion != v1.PreviewLifecycleTransportVersion || len(manifest.PreviewLifecycleProfiles) != 2 ||
		manifest.PreviewLifecycleProfiles[0].BlueprintID != "a" || manifest.PreviewLifecycleProfiles[1].BlueprintID != "z" ||
		manifest.PreviewLifecycleProfiles[0].MaxExtensionMS == nil || *manifest.PreviewLifecycleProfiles[0].MaxExtensionMS != maxExtension {
		t.Fatalf("manifest = %+v", manifest)
	}
	manifest.PreviewLifecycleProfiles[0].Actions[0] = "changed"
	*manifest.PreviewLifecycleProfiles[0].MaxExtensionMS = 1
	if detector.PreviewLifecycleProfiles[1].Actions[0] != v1.PreviewLifecycleDestroy || *detector.PreviewLifecycleProfiles[1].MaxExtensionMS != maxExtension {
		t.Fatal("capability collection shared mutable preview lifecycle state")
	}
}
