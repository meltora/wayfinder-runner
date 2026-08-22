package capability

import (
	"context"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestCollectAdvertisesSeparateExactReconciliationInstallations(t *testing.T) {
	d := New("runner-1", "test", nil, nil)
	d.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	d.ProviderReviewerAssignmentReconciliationInstallations = []v1.ProviderReviewerAssignmentReconciliationInstallationCapability{{InstallationID: "z", Provider: "gitlab", ProviderInstanceKey: "gitlab.example.com"}, {InstallationID: "a", Provider: "github", ProviderInstanceKey: "github.com"}}
	m := d.Collect(context.Background())
	if m.ProviderReviewerAssignmentReconciliationTransportVersion != 1 || !reflect.DeepEqual(m.ProviderReviewerAssignmentReconciliationProviders, []string{"github", "gitlab"}) || m.ProviderReviewerAssignmentReconciliationInstallations[0].InstallationID != "a" {
		t.Fatalf("manifest=%#v", m)
	}
	if m.ProviderReviewerAssignmentTransportVersion != 0 || len(m.ProviderReviewerAssignmentInstallations) != 0 {
		t.Fatal("read capability incorrectly implied write capability")
	}
}

func TestCollectAdvertisesProviderReviewObservationWithoutWriteCapability(t *testing.T) {
	d := New("runner-1", "test", nil, nil)
	d.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	d.ProviderReviewObservationInstallations = []v1.ProviderReviewObservationInstallationCapability{{InstallationID: "b", Provider: "gitlab", ProviderInstanceKey: "gitlab.example.com"}, {InstallationID: "a", Provider: "github", ProviderInstanceKey: "github.com"}}
	m := d.Collect(context.Background())
	if m.ProviderReviewObservationTransportVersion != 1 || !reflect.DeepEqual(m.ProviderReviewObservationProviders, []string{"github", "gitlab"}) || m.ProviderReviewObservationInstallations[0].InstallationID != "a" {
		t.Fatalf("manifest=%#v", m)
	}
	if m.ProviderReviewerAssignmentTransportVersion != 0 {
		t.Fatal("read observation implied write capability")
	}
}

func TestCollectAdvertisesFeedbackContentAsSeparateReadCapability(t *testing.T) {
	d := New("runner-1", "test", nil, nil)
	d.LookPath = func(string) (string, error) { return "", errNotFoundForCapabilityTest{} }
	d.ProviderReviewFeedbackContentInstallations = []v1.ProviderReviewFeedbackContentInstallationCapability{{InstallationID: "a", Provider: "github", ProviderInstanceKey: "github.com"}}
	m := d.Collect(context.Background())
	if m.ProviderReviewFeedbackContentTransportVersion != 1 || !reflect.DeepEqual(m.ProviderReviewFeedbackContentProviders, []string{"github"}) || m.ProviderReviewFeedbackContentInstallations[0].InstallationID != "a" {
		t.Fatalf("manifest=%#v", m)
	}
	if m.ProviderReviewObservationTransportVersion != 0 || m.ProviderReviewerAssignmentTransportVersion != 0 {
		t.Fatal("feedback read implied another capability")
	}
}

type errNotFoundForCapabilityTest struct{}

func (errNotFoundForCapabilityTest) Error() string { return "not found" }
