package capability

import (
	"context"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestCollectAdvertisesProviderPullRequestCapabilitySeparately(t *testing.T) {
	d := New("runner-1", "test", nil, nil)
	d.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	m := d.Collect(context.Background())
	if m.ProviderPullRequestTransportVersion != "" || m.ProviderPullRequestInstallations != nil {
		t.Fatalf("manifest without installations = %#v", m)
	}
	d.ProviderPullRequestInstallations = []v1.ProviderPullRequestInstallationCapability{{InstallationID: "b", Provider: "github", ProviderInstanceKey: "github.com"}, {InstallationID: "a", Provider: "github", ProviderInstanceKey: "github.com"}}
	m = d.Collect(context.Background())
	if m.ProviderPullRequestTransportVersion != v1.ProviderPullRequestTransportVersion || !reflect.DeepEqual(m.ProviderPullRequestProviders, []string{"github"}) || m.ProviderPullRequestInstallations[0].InstallationID != "a" {
		t.Fatalf("manifest = %#v", m)
	}
	if m.ProviderReviewObservationTransportVersion != 0 || m.ProviderReviewFeedbackContentTransportVersion != 0 {
		t.Fatal("pull-request capability implied another capability")
	}
}
