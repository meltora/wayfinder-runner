package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validLeaseRenewalEvent() v1.LeaseRenewalEvent {
	return v1.LeaseRenewalEvent{
		Kind: "lease_renewal", ManifestKind: v1.JobKindPlan, LeaseID: "lease-1",
		JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 7, ObservedAt: time.Now().UTC(),
	}
}

func TestLeaseRenewalEventRejectsInvalidAuthority(t *testing.T) {
	t.Parallel()
	if err := LeaseRenewalEvent(validLeaseRenewalEvent()); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*v1.LeaseRenewalEvent){
		"kind":        func(event *v1.LeaseRenewalEvent) { event.Kind = "renew" },
		"lease":       func(event *v1.LeaseRenewalEvent) { event.LeaseID = "bad/id" },
		"kind value":  func(event *v1.LeaseRenewalEvent) { event.ManifestKind = "delete" },
		"manifest":    func(event *v1.LeaseRenewalEvent) { event.ManifestID = "" },
		"digest":      func(event *v1.LeaseRenewalEvent) { event.ManifestDigest = strings.Repeat("A", 64) },
		"fence":       func(event *v1.LeaseRenewalEvent) { event.FencingToken = 0 },
		"large fence": func(event *v1.LeaseRenewalEvent) { event.FencingToken = 1 << 53 },
		"observed":    func(event *v1.LeaseRenewalEvent) { event.ObservedAt = time.Time{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			event := validLeaseRenewalEvent()
			mutate(&event)
			if err := LeaseRenewalEvent(event); err == nil {
				t.Fatalf("invalid renewal accepted: %#v", event)
			}
		})
	}
}
