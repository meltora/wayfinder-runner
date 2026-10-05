package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReviewFeedbackContinuationJobScopeDigestMatchesControlPlaneVector(t *testing.T) {
	job := JobSpec{
		JobID: "workflow-1", AttemptID: "review-feedback-continuation-attempt-1",
		ProjectID: "project-1", Kind: JobKindBuild, LeaseID: "review-feedback-continuation-lease-1",
		ManifestID: "execution-manifest-2", ManifestDigest: strings.Repeat("f", 64), WorkflowRevision: 12,
		FencingToken: 23, LeaseExpiresAt: time.Date(2026, 8, 22, 13, 0, 0, 0, time.UTC),
		Repositories:         []JobRepository{{RepositoryID: "repository-1", BaseBranch: "main", Primary: true}},
		Ticket:               TicketContext{Key: "WAY-123", Summary: "Address authenticated provider review feedback"},
		ApprovedPlanRevision: 3, ContinuationCount: 5, ResumeSessionID: "agent-session-1",
	}
	const expected = "9765d535c79c068bda783a28eb81e4f60e0cfee1a8fc799c5b918dc520b9b8be"
	if got := ReviewFeedbackContinuationJobScopeDigest(job); got != expected {
		t.Fatalf("scope digest = %q, want %q", got, expected)
	}
}

func TestReviewFeedbackContinuationJobScopeDigestNormalizesDefaultsAndSetOrdering(t *testing.T) {
	left := JobSpec{
		ProjectID: "project-1", Kind: JobKindBuild,
		Repositories: []JobRepository{{RepositoryID: "a-repository", Primary: true}, {RepositoryID: "Z-repository", BaseBranch: "main"}},
		Ticket:       TicketContext{Key: "WAY-1", Summary: "Summary"}, ApprovedPlanRevision: 3,
		RequiredChecks: []string{"a-check", "Z-check"},
	}
	right := left
	right.Repositories = []JobRepository{{RepositoryID: "Z-repository", BaseBranch: "main", Primary: false}, {RepositoryID: "a-repository", BaseBranch: "", Primary: true}}
	right.RequiredChecks = []string{"Z-check", "a-check"}
	if got, want := ReviewFeedbackContinuationJobScopeDigest(left), ReviewFeedbackContinuationJobScopeDigest(right); got == "" || got != want {
		t.Fatalf("scope digest normalization mismatch: %q != %q", got, want)
	}
}

func TestJobSpecPreservesExplicitLegacyReviewCommentPresenceAtTheBoundary(t *testing.T) {
	var job JobSpec
	if err := json.Unmarshal([]byte(`{"review_comment":""}`), &job); err != nil {
		t.Fatal(err)
	}
	if !job.HasReviewCommentField() {
		t.Fatal("explicit empty legacy review_comment became indistinguishable from absence")
	}
	if err := json.Unmarshal([]byte(`{"unexpected":true}`), &job); err == nil {
		t.Fatal("custom job decoder weakened unknown-field rejection")
	}
}

func TestReviewFeedbackContinuationCommandPreservesNestedAgentRunAuthority(t *testing.T) {
	authority := AgentRunAuthority{
		SchemaVersion: 1,
		AgentRunID:    "review-continuation-agent-1",
		StepRunID:     "review-continuation-step-1",
		StepSequence:  4,
	}
	command := CommandPayload{
		Command: CmdReviewFeedbackContinuation,
		Job: &JobSpec{
			JobID: "workflow-1", AgentRunAuthority: &authority,
		},
		ReviewFeedbackContinuation: &ReviewFeedbackContinuationAuthority{},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CommandPayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Job == nil || decoded.Job.AgentRunAuthority == nil ||
		*decoded.Job.AgentRunAuthority != authority {
		t.Fatalf("decoded AgentRun authority = %+v, want %+v", decoded.Job, authority)
	}
}

func TestReviewFeedbackContinuationAuthorityAndCommandMatchControlPlaneVectors(t *testing.T) {
	job := JobSpec{
		JobID: "workflow-1", AttemptID: "review-feedback-continuation-attempt-1",
		ProjectID: "project-1", Kind: JobKindBuild, LeaseID: "review-feedback-continuation-lease-1",
		ManifestID: "execution-manifest-2", ManifestDigest: strings.Repeat("f", 64), WorkflowRevision: 12,
		FencingToken: 23, LeaseExpiresAt: time.Date(2026, 8, 22, 13, 0, 0, 0, time.UTC),
		Repositories:         []JobRepository{{RepositoryID: "repository-1", BaseBranch: "main", Primary: true}},
		Ticket:               TicketContext{Key: "WAY-123", Summary: "Address authenticated provider review feedback"},
		ApprovedPlanRevision: 3, ContinuationCount: 5, ResumeSessionID: "agent-session-1",
	}
	authority := ReviewFeedbackContinuationAuthority{
		SchemaVersion: 1, Purpose: ReviewFeedbackContinuationPurpose,
		ReviewFeedbackContinuationID: "review-feedback-continuation-" + strings.Repeat("1", 64),
		OrganizationID:               "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
		SourceWorkflowRevision: 11, ResultingWorkflowRevision: 12, SourceAttemptID: "source-attempt-1",
		SourceExecutionManifestID: "execution-manifest-1", SourceExecutionManifestDigest: strings.Repeat("2", 64),
		AssignmentDeliveryID: "assignment-delivery-1", AssignmentDeliveryStateRevision: 2,
		AssignmentObservationID: "assignment-observation-1", ObservedReviewerID: "observed-reviewer-1",
		ProviderReviewObservationID:                    "provider-review-observation-1",
		ProviderReviewObservationResultPayloadChecksum: strings.Repeat("3", 64),
		ContentSource: ReviewFeedbackContinuationContentSource, ProviderReviewEventID: "7001", ProviderReviewEventRevision: 3,
		ReviewEventKind: ProviderReviewObservationChangeRequest, ReviewState: ProviderReviewObservationChangesRequested,
		ReviewEventOccurredAt:        time.Date(2026, 8, 22, 11, 58, 0, 0, time.UTC),
		ProviderReviewEvidenceDigest: strings.Repeat("4", 64), EvidenceObservedAt: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
		ProviderReviewFeedbackContentOperationID:           "provider-review-feedback-content-operation-" + strings.Repeat("5", 64),
		ProviderReviewFeedbackContentResultPayloadChecksum: strings.Repeat("6", 64),
		FeedbackContentRequestDigest:                       "031c23af2a0458b941bf351da9d5baa489842200c48308405b475503012cfce1",
		FeedbackContentObservedAt:                          time.Date(2026, 8, 22, 12, 4, 0, 0, time.UTC),
		ContentDigest:                                      "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527", ContentByteCount: 25,
		ProviderAccountID: "12345", UserID: "user-1", OrganizationMembershipID: "membership-1",
		ReviewerIdentityID: "reviewer-identity-1", InstallationID: "installation-1",
		InstallationStateRevision: 4, InstallationAuthorityRevision: 7, InstallationAuthorityDigest: strings.Repeat("c", 64),
		Provider: "github", ProviderInstanceKey: "github.com", RepositoryID: "repository-1", ProviderRepositoryID: "98765",
		ExternalRequestID: "42", ExpectedHeadSHA: strings.Repeat("b", 40), ApprovedScopeDigest: ReviewFeedbackContinuationJobScopeDigest(job),
		ContinuationIndex: 5, RemainingCommandBudget: 40,
		IdempotencyKey: "review-feedback-continuation-" + strings.Repeat("b", 64), LeaseID: job.LeaseID,
		FencingToken: 23, LeaseExpiresAt: job.LeaseExpiresAt, DeadlineAt: time.Date(2026, 8, 22, 12, 55, 0, 0, time.UTC),
		Feedback: ReviewFeedbackContinuationFeedback{
			Trust: ReviewFeedbackContinuationUntrustedText, Authority: ReviewFeedbackContinuationNonAuthoritative,
			ContentEncoding: "utf-8", ContentFormat: "plain_text", UnicodeNormalization: "NFC", LineEndings: "LF",
			ContentDigest:    "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527",
			ContentByteCount: 25, Content: "Café\nFix the retry path.",
		},
	}
	raw, err := json.Marshal(CommandPayload{Command: CmdReviewFeedbackContinuation, Job: &job, ReviewFeedbackContinuation: &authority})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["job"].(map[string]any)["lease_expires_at"] = "2026-08-22T13:00:00.000Z"
	authorityWire := wire["review_feedback_continuation"].(map[string]any)
	for field, value := range map[string]string{
		"review_event_occurred_at":     "2026-08-22T11:58:00.000Z",
		"evidence_observed_at":         "2026-08-22T12:00:00.000Z",
		"feedback_content_observed_at": "2026-08-22T12:04:00.000Z",
		"deadline_at":                  "2026-08-22T12:55:00.000Z",
		"lease_expires_at":             "2026-08-22T13:00:00.000Z",
	} {
		authorityWire[field] = value
	}
	if got, err := CommandContentDigest(authorityWire); err != nil || got != "edb9a73c4479eb9a74af2ba1ac4e2dffcd7dd1fa2afed0fde66a8974a0fc6525" {
		t.Fatalf("authority digest=%q err=%v", got, err)
	}
	if got, err := CommandContentDigest(wire); err != nil || got != "0624df68475d04d4e192465bf72ccc8a66bd493a1dba693b3d8ec8be3d331a6e" {
		encoded, _ := json.Marshal(wire)
		t.Fatalf("command digest=%q err=%v wire=%s", got, err, encoded)
	}
}
