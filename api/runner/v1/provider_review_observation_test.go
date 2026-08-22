package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProviderReviewObservationCanonicalVectors(t *testing.T) {
	subjects := providerReviewObservationVectorSubjects()
	if got := ProviderReviewObservationSubjectIdentitySetDigest(subjects); got != "d99f5dbbfd0f6bc84db55559f0f967190607e0fff2b922d4f48237e5edf328b3" {
		t.Fatalf("subject digest=%s", got)
	}
	identity := ProviderReviewObservationDerivedReviewEventIdentity{Provider: "gitlab", ProviderInstanceKey: "gitlab.com", ProviderRepositoryID: "12345", ExternalRequestID: "17", ReviewEventKind: ProviderReviewObservationReviewerState, ProviderAccountID: "42", OccurredAt: time.Date(2030, 8, 21, 11, 58, 0, 0, time.UTC)}
	if got := ProviderReviewObservationDerivedReviewEventID(identity); got != "derived-16c64d8a085c8228e39b25adb13546e4a956f015ac47ace2ffc7317757ff08fd" {
		t.Fatalf("derived=%s", got)
	}
}

func TestProviderReviewObservationSubjectIdentitySetDigestCanonicalizesWithoutMutation(t *testing.T) {
	subjects := providerReviewObservationVectorSubjects()
	reversed := []ProviderReviewObservationSubject{subjects[1], subjects[0]}
	want := ProviderReviewObservationSubjectIdentitySetDigest(subjects)
	if got := ProviderReviewObservationSubjectIdentitySetDigest(reversed); got != want {
		t.Fatalf("permuted digest=%s, want %s", got, want)
	}
	if reversed[0].ProviderAccountID != "99" || reversed[1].ProviderAccountID != "42" {
		t.Fatalf("digest helper mutated caller order: %#v", reversed)
	}
}

func TestProviderReviewObservationSuccessWireIncludesFalseAndEmptyCollections(t *testing.T) {
	out := ProviderReviewObservationResultOutcome{Kind: ProviderReviewObservationSucceeded, RequestState: ProviderReviewObservationRequestOpen, ObservedHeadSHA: strings.Repeat("1", 40), StaleHead: false, SnapshotSemantics: "current_state", SnapshotComplete: true, Collections: &ProviderReviewObservationCollections{}}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, part := range []string{`"stale_head":false`, `"snapshot_complete":true`, `"review_events":[]`, `"comments":[]`} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %s in %s", part, text)
		}
	}
	if strings.Contains(text, "code") || strings.Contains(text, "retry_after_ms") {
		t.Fatalf("error field leaked: %s", text)
	}
}

func providerReviewObservationVectorSubjects() []ProviderReviewObservationSubject {
	return []ProviderReviewObservationSubject{{ReviewerIdentityID: "reviewer-identity-a", UserID: "user-a", OrganizationMembershipID: "membership-a", IdentityStateRevision: 3, IdentityRevision: 2, ProviderAccountProofID: "provider-account-proof-a", ProviderAccountProofStateRevision: 4, ProviderAccountProofRevision: 2, ProviderAccountProofDigest: strings.Repeat("b", 64), ProviderAccountProofExpiresAt: time.Date(2030, 8, 21, 13, 0, 0, 0, time.UTC), IdentityVerificationID: "identity-verification-a", IdentityVerificationDigest: strings.Repeat("6", 64), IdentityVerificationExpiresAt: time.Date(2030, 8, 21, 12, 30, 0, 0, time.UTC), ProviderAccountID: "42"}, {ReviewerIdentityID: "reviewer-identity-b", UserID: "user-b", OrganizationMembershipID: "membership-b", IdentityStateRevision: 5, IdentityRevision: 4, ProviderAccountProofID: "provider-account-proof-b", ProviderAccountProofStateRevision: 6, ProviderAccountProofRevision: 4, ProviderAccountProofDigest: strings.Repeat("c", 64), ProviderAccountProofExpiresAt: time.Date(2030, 8, 21, 13, 0, 0, 0, time.UTC), IdentityVerificationID: "identity-verification-b", IdentityVerificationDigest: strings.Repeat("7", 64), IdentityVerificationExpiresAt: time.Date(2030, 8, 21, 12, 30, 0, 0, time.UTC), ProviderAccountID: "99"}}
}
