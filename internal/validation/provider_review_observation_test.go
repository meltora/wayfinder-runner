package validation

import (
	v1 "github.com/example/app-runner/api/runner/v1"
	"strings"
	"testing"
	"time"
)

func validProviderReviewObservationOperation() v1.ProviderReviewObservationOperation {
	lease := time.Date(2030, 8, 21, 12, 4, 0, 0, time.UTC)
	subjects := []v1.ProviderReviewObservationSubject{{ReviewerIdentityID: "identity-1", UserID: "user-1", OrganizationMembershipID: "membership-1", IdentityStateRevision: 1, IdentityRevision: 1, ProviderAccountProofID: "proof-1", ProviderAccountProofStateRevision: 1, ProviderAccountProofRevision: 1, ProviderAccountProofDigest: strings.Repeat("1", 64), ProviderAccountProofExpiresAt: lease.Add(time.Hour), IdentityVerificationID: "verification-1", IdentityVerificationDigest: strings.Repeat("2", 64), IdentityVerificationExpiresAt: lease.Add(time.Minute), ProviderAccountID: "42"}}
	return v1.ProviderReviewObservationOperation{ProviderReviewObservationAuthority: v1.ProviderReviewObservationAuthority{SchemaVersion: 1, ProviderReviewObservationOperationID: "provider-review-observation-operation-" + strings.Repeat("a", 64), OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "run-1", AssignmentDeliveryID: "delivery-1", AssignmentDeliveryStateRevision: 1, AssignmentPlanID: "plan-1", AssignmentPlanDigest: strings.Repeat("3", 64), AssignmentObservationID: "observation-1", AssignmentResultPayloadChecksum: strings.Repeat("4", 64), SourcePublicationObservationID: "source-observation-1", RepositoryID: "repository-1", ProviderRepositoryID: "123", ExternalRequestID: "7", ExpectedHeadSHA: "0123456789abcdef0123456789abcdef01234567", InstallationID: "installation-1", InstallationStateRevision: 1, InstallationAuthorityRevision: 1, InstallationAuthorityDigest: strings.Repeat("5", 64), Provider: "github", ProviderInstanceKey: "github.com", IdempotencyKey: "provider-review-observation-" + strings.Repeat("6", 64), SubjectIdentitySetDigest: v1.ProviderReviewObservationSubjectIdentitySetDigest(subjects), MaxCollectionPages: 10, CollectionPageSize: 100, OperationDigest: strings.Repeat("7", 64), CommandDigest: strings.Repeat("8", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: lease}, Subjects: subjects}
}
func TestProviderReviewObservationOperationStrictAuthority(t *testing.T) {
	valid := validProviderReviewObservationOperation()
	if err := ProviderReviewObservationOperation(valid); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*v1.ProviderReviewObservationOperation){"digest": func(v *v1.ProviderReviewObservationOperation) { v.SubjectIdentitySetDigest = strings.Repeat("f", 64) }, "bounds": func(v *v1.ProviderReviewObservationOperation) { v.MaxCollectionPages = 11 }, "path": func(v *v1.ProviderReviewObservationOperation) { v.ProviderRepositoryPath = "owner/repo" }, "expiry": func(v *v1.ProviderReviewObservationOperation) {
		v.Subjects[0].IdentityVerificationExpiresAt = v.LeaseExpiresAt.Add(-time.Second)
		v.SubjectIdentitySetDigest = v1.ProviderReviewObservationSubjectIdentitySetDigest(v.Subjects)
	}, "account": func(v *v1.ProviderReviewObservationOperation) {
		v.Subjects[0].ProviderAccountID = "042"
		v.SubjectIdentitySetDigest = v1.ProviderReviewObservationSubjectIdentitySetDigest(v.Subjects)
	}}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := valid
			value.Subjects = append([]v1.ProviderReviewObservationSubject(nil), valid.Subjects...)
			mutate(&value)
			if ProviderReviewObservationOperation(value) == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestProviderReviewObservationOperationAcceptsExactZeroInstallationRevisions(t *testing.T) {
	value := validProviderReviewObservationOperation()
	value.InstallationStateRevision = 0
	value.InstallationAuthorityRevision = 0
	if err := ProviderReviewObservationOperation(value); err != nil {
		t.Fatalf("zero is a legitimate exact initial revision: %v", err)
	}
}

func TestProviderReviewObservationOperationAcceptsExactZeroSubjectStateRevisions(t *testing.T) {
	value := validProviderReviewObservationOperation()
	value.Subjects[0].IdentityStateRevision = 0
	value.Subjects[0].ProviderAccountProofStateRevision = 0
	value.SubjectIdentitySetDigest = v1.ProviderReviewObservationSubjectIdentitySetDigest(value.Subjects)
	if err := ProviderReviewObservationOperation(value); err != nil {
		t.Fatalf("zero is a legitimate active state revision: %v", err)
	}
}
func TestProviderReviewObservationResultStrictNormalizedShape(t *testing.T) {
	op := validProviderReviewObservationOperation()
	observed := op.LeaseExpiresAt.Add(-time.Minute)
	event := v1.ProviderReviewObservationResultEvent{Kind: v1.EventProviderReviewObservationResult, ProviderReviewObservationAuthority: op.ProviderReviewObservationAuthority, CommandMessageID: "provider-review-observation-command-" + strings.Repeat("9", 64), ObservedAt: observed, Outcome: v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationSucceeded, RequestState: v1.ProviderReviewObservationRequestOpen, ObservedHeadSHA: op.ExpectedHeadSHA, SnapshotSemantics: "current_state", SnapshotComplete: true, Collections: &v1.ProviderReviewObservationCollections{ReviewEvents: v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: 1, ItemsObserved: 1}, Comments: v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: 1, ItemsObserved: 1}}, ReviewEvents: []v1.ProviderReviewObservationReviewEvent{{ProviderReviewEventID: "7001", ProviderReviewEventRevision: 1, ProviderAccountID: "42", ReviewEventKind: v1.ProviderReviewObservationReview, ReviewState: v1.ProviderReviewObservationApproved, OccurredAt: observed.Add(-time.Minute), CommitSHA: op.ExpectedHeadSHA, ContentDigest: strings.Repeat("a", 64), ContentByteCount: 1}}, Comments: []v1.ProviderReviewObservationComment{{ProviderCommentID: "8001", ProviderCommentRevision: 1, ProviderAccountID: "42", ProviderCommentNamespace: v1.ProviderReviewObservationReviewNamespace, CommentKind: v1.ProviderReviewObservationInline, CreatedAt: observed.Add(-time.Minute), UpdatedAt: observed, Position: &v1.ProviderReviewObservationCommentPosition{FilePath: "src/app.go", Line: 1, Side: "right", CommitSHA: op.ExpectedHeadSHA}, ContentDigest: strings.Repeat("b", 64), ContentByteCount: 2}}}}
	if err := ProviderReviewObservationResultEvent(event); err != nil {
		t.Fatal(err)
	}
	invalid := event
	invalid.Outcome.StaleHead = true
	if ProviderReviewObservationResultEvent(invalid) == nil {
		t.Fatal("accepted false stale head")
	}
	invalid = event
	invalid.Outcome.Comments[0].Deleted = true
	if ProviderReviewObservationResultEvent(invalid) == nil {
		t.Fatal("accepted github tombstone")
	}
	retry := event
	retry.Outcome = v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationRetryable, Code: v1.ProviderReviewObservationProviderUnavailable}
	if err := ProviderReviewObservationResultEvent(retry); err != nil {
		t.Fatal(err)
	}
	permanent := event
	permanent.Outcome = v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationPermanent, Code: v1.ProviderReviewObservationIncomplete}
	if err := ProviderReviewObservationResultEvent(permanent); err != nil {
		t.Fatal(err)
	}
}

func TestProviderReviewObservationMetadataValidators(t *testing.T) {
	if !metadataID("stable-id") {
		t.Fatal("stable ID rejected")
	}
	for _, value := range []string{"", " leading", "trailing ", "https://provider.example/id", "line\nbreak", strings.Repeat("x", 513)} {
		if metadataID(value) {
			t.Fatalf("metadata ID accepted: %q", value)
		}
	}
	for _, state := range []v1.ProviderReviewObservationReviewState{v1.ProviderReviewObservationUnreviewed, v1.ProviderReviewObservationReviewStarted, v1.ProviderReviewObservationReviewed, v1.ProviderReviewObservationCommented, v1.ProviderReviewObservationApproved, v1.ProviderReviewObservationChangesRequested, v1.ProviderReviewObservationDismissed, v1.ProviderReviewObservationUnapproved} {
		if !reviewState(state) {
			t.Fatalf("state=%s", state)
		}
	}
	if reviewState("unknown") {
		t.Fatal("unknown state accepted")
	}
	for _, kind := range []v1.ProviderReviewObservationReviewEventKind{v1.ProviderReviewObservationReview, v1.ProviderReviewObservationReviewerState, v1.ProviderReviewObservationApproval, v1.ProviderReviewObservationChangeRequest, v1.ProviderReviewObservationDismissal} {
		if !reviewKind(kind) {
			t.Fatalf("kind=%s", kind)
		}
	}
	if reviewKind("unknown") {
		t.Fatal("unknown kind accepted")
	}
	if ProviderReviewObservationCommandMessageID("provider-review-observation-command-"+strings.Repeat("a", 64)) != nil || ProviderReviewObservationCommandMessageID("bad") == nil {
		t.Fatal("command ID validation mismatch")
	}
}

func TestProviderReviewObservationEvidenceValidators(t *testing.T) {
	op := validProviderReviewObservationOperation()
	observed := op.LeaseExpiresAt.Add(-time.Minute)
	baseReview := v1.ProviderReviewObservationReviewEvent{ProviderReviewEventID: "1", ProviderReviewEventRevision: 1, ProviderAccountID: "42", ReviewEventKind: v1.ProviderReviewObservationApproval, ReviewState: v1.ProviderReviewObservationApproved, OccurredAt: observed.Add(-time.Minute), ContentDigest: strings.Repeat("a", 64)}
	if err := reviewEvent(op.ProviderReviewObservationAuthority, baseReview, observed); err != nil {
		t.Fatal(err)
	}
	badNative := baseReview
	badNative.ProviderReviewEventID = "01"
	if reviewEvent(op.ProviderReviewObservationAuthority, badNative, observed) == nil {
		t.Fatal("noncanonical native ID accepted")
	}
	derived := baseReview
	derived.ProviderReviewEventID = v1.ProviderReviewObservationDerivedReviewEventID(v1.ProviderReviewObservationDerivedReviewEventIdentity{Provider: op.Provider, ProviderInstanceKey: op.ProviderInstanceKey, ProviderRepositoryID: op.ProviderRepositoryID, ExternalRequestID: op.ExternalRequestID, ReviewEventKind: derived.ReviewEventKind, ProviderAccountID: derived.ProviderAccountID, OccurredAt: derived.OccurredAt})
	if err := reviewEvent(op.ProviderReviewObservationAuthority, derived, observed); err != nil {
		t.Fatal(err)
	}
	derived.ProviderReviewEventID = "derived-" + strings.Repeat("f", 64)
	if reviewEvent(op.ProviderReviewObservationAuthority, derived, observed) == nil {
		t.Fatal("forged derived ID accepted")
	}

	baseComment := v1.ProviderReviewObservationComment{ProviderCommentID: "1", ProviderCommentRevision: 1, ProviderAccountID: "42", ProviderCommentNamespace: v1.ProviderReviewObservationIssue, CommentKind: v1.ProviderReviewObservationGeneral, CreatedAt: observed.Add(-time.Minute), UpdatedAt: observed, ContentDigest: strings.Repeat("b", 64)}
	if err := comment("github", baseComment, observed); err != nil {
		t.Fatal(err)
	}
	inline := baseComment
	inline.CommentKind = v1.ProviderReviewObservationInline
	inline.Position = &v1.ProviderReviewObservationCommentPosition{FilePath: "src/app.go", Line: 1, Side: "left", CommitSHA: op.ExpectedHeadSHA}
	if err := comment("github", inline, observed); err != nil {
		t.Fatal(err)
	}
	reply := baseComment
	reply.CommentKind = v1.ProviderReviewObservationThreadReply
	reply.ParentCommentID = "2"
	if err := comment("github", reply, observed); err != nil {
		t.Fatal(err)
	}
	tombstone := baseComment
	tombstone.ProviderAccountID = "bb-account-42"
	tombstone.ProviderCommentNamespace = v1.ProviderReviewObservationPullRequest
	tombstone.Deleted = true
	if err := comment("bitbucket", tombstone, observed); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*v1.ProviderReviewObservationComment){
		"namespace":               func(c *v1.ProviderReviewObservationComment) { c.ProviderCommentNamespace = "unknown" },
		"kind":                    func(c *v1.ProviderReviewObservationComment) { c.CommentKind = "unknown" },
		"inline missing position": func(c *v1.ProviderReviewObservationComment) { c.CommentKind = v1.ProviderReviewObservationInline },
		"reply missing parent":    func(c *v1.ProviderReviewObservationComment) { c.CommentKind = v1.ProviderReviewObservationThreadReply },
		"bad parent":              func(c *v1.ProviderReviewObservationComment) { c.ParentCommentID = "01" },
		"bad position": func(c *v1.ProviderReviewObservationComment) {
			c.Position = &v1.ProviderReviewObservationCommentPosition{FilePath: "../secret", Line: 1, Side: "right", CommitSHA: op.ExpectedHeadSHA}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := baseComment
			mutate(&value)
			if comment("github", value, observed) == nil {
				t.Fatal("accepted")
			}
		})
	}
	if collection(v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: 1, ItemsObserved: 1}, 1) != nil {
		t.Fatal("valid collection rejected")
	}
	for _, value := range []v1.ProviderReviewObservationCollectionCompleteness{{}, {Complete: true, PagesRead: 11}, {Complete: true, PagesRead: 1, ItemsObserved: 2}} {
		if collection(value, 1) == nil {
			t.Fatalf("collection accepted: %#v", value)
		}
	}
}

func TestProviderReviewObservationPermanentCodeAllowlist(t *testing.T) {
	for _, code := range []v1.ProviderReviewObservationErrorCode{v1.ProviderReviewObservationRequestNotFound, v1.ProviderReviewObservationRepositoryMismatch, v1.ProviderReviewObservationInstallationInactive, v1.ProviderReviewObservationInstallationAuthorityChanged, v1.ProviderReviewObservationInstallationPermissionDenied, v1.ProviderReviewObservationSubjectIdentityChanged, v1.ProviderReviewObservationSubjectIdentityExpired, v1.ProviderReviewObservationProviderAccountProofChanged, v1.ProviderReviewObservationProviderAccountProofExpired, v1.ProviderReviewObservationUnsupportedProviderInstance, v1.ProviderReviewObservationIncomplete, v1.ProviderReviewObservationInvalidProviderResponse} {
		if !reviewObservationPermanent(code) {
			t.Fatalf("code=%s", code)
		}
	}
	if reviewObservationPermanent(v1.ProviderReviewObservationRateLimited) {
		t.Fatal("retryable code accepted")
	}
}

func TestProviderReviewObservationResultRejectsEnvelopeAndOutcomeDrift(t *testing.T) {
	op := validProviderReviewObservationOperation()
	base := v1.ProviderReviewObservationResultEvent{Kind: v1.EventProviderReviewObservationResult, ProviderReviewObservationAuthority: op.ProviderReviewObservationAuthority, CommandMessageID: "provider-review-observation-command-" + strings.Repeat("9", 64), ObservedAt: op.LeaseExpiresAt, Outcome: v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationPermanent, Code: v1.ProviderReviewObservationRequestNotFound}}
	if err := ProviderReviewObservationResultEvent(base); err != nil {
		t.Fatal(err)
	}
	badKind := base
	badKind.Kind = "other"
	if ProviderReviewObservationResultEvent(badKind) == nil {
		t.Fatal("bad event kind accepted")
	}
	late := base
	late.ObservedAt = op.LeaseExpiresAt.Add(time.Nanosecond)
	if ProviderReviewObservationResultEvent(late) == nil {
		t.Fatal("late observation accepted")
	}
	unknown := base
	unknown.Outcome = v1.ProviderReviewObservationResultOutcome{Kind: "unknown"}
	if ProviderReviewObservationResultEvent(unknown) == nil {
		t.Fatal("unknown outcome accepted")
	}
}
