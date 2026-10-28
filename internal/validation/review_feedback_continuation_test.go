package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validReviewFeedbackContinuationCommand() (v1.JobSpec, v1.ReviewFeedbackContinuationAuthority) {
	job := v1.JobSpec{
		JobID: "workflow-1", AttemptID: "review-feedback-continuation-attempt-1",
		ProjectID: "project-1", Kind: v1.JobKindBuild, LeaseID: "review-feedback-continuation-lease-1",
		ManifestID: "execution-manifest-2", ManifestDigest: strings.Repeat("f", 64), WorkflowRevision: 12,
		FencingToken: 23, LeaseExpiresAt: time.Date(2030, 8, 22, 13, 0, 0, 0, time.UTC),
		Repositories:         []v1.JobRepository{{RepositoryID: "repository-1", BaseBranch: "main", Primary: true}},
		Ticket:               v1.TicketContext{Key: "WAY-123", Summary: "Address authenticated provider review feedback"},
		ApprovedPlanRevision: 3, ContinuationCount: 5, ResumeSessionID: "agent-session-1",
		ApprovedPlan: &v1.ApprovedPlan{RevisionID: "revision-3", Revision: 3, Digest: strings.Repeat("c", 64), Content: validPlanContent()},
	}
	authority := v1.ReviewFeedbackContinuationAuthority{
		SchemaVersion: 1, Purpose: v1.ReviewFeedbackContinuationPurpose,
		ReviewFeedbackContinuationID: "review-feedback-continuation-" + strings.Repeat("1", 64),
		OrganizationID:               "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
		SourceWorkflowRevision: 11, ResultingWorkflowRevision: 12,
		SourceAttemptID: "source-attempt-1", SourceExecutionManifestID: "execution-manifest-1",
		SourceExecutionManifestDigest: strings.Repeat("2", 64), AssignmentDeliveryID: "assignment-delivery-1",
		AssignmentDeliveryStateRevision: 2, AssignmentObservationID: "assignment-observation-1",
		ObservedReviewerID: "observed-reviewer-1", ProviderReviewObservationID: "provider-review-observation-1",
		ProviderReviewObservationResultPayloadChecksum: strings.Repeat("3", 64),
		ContentSource: v1.ReviewFeedbackContinuationContentSource, ProviderReviewEventID: "7001",
		ProviderReviewEventRevision: 3, ReviewEventKind: v1.ProviderReviewObservationChangeRequest,
		ReviewState:                  v1.ProviderReviewObservationChangesRequested,
		ReviewEventOccurredAt:        time.Date(2030, 8, 22, 11, 58, 0, 0, time.UTC),
		ProviderReviewEvidenceDigest: strings.Repeat("4", 64), EvidenceObservedAt: time.Date(2030, 8, 22, 12, 0, 0, 0, time.UTC),
		ProviderReviewFeedbackContentOperationID:           "provider-review-feedback-content-operation-" + strings.Repeat("5", 64),
		ProviderReviewFeedbackContentResultPayloadChecksum: strings.Repeat("6", 64),
		FeedbackContentRequestDigest:                       "031c23af2a0458b941bf351da9d5baa489842200c48308405b475503012cfce1",
		FeedbackContentObservedAt:                          time.Date(2030, 8, 22, 12, 4, 0, 0, time.UTC),
		ContentDigest:                                      "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527", ContentByteCount: 25,
		ProviderAccountID: "12345", UserID: "user-1", OrganizationMembershipID: "membership-1",
		ReviewerIdentityID: "reviewer-identity-1", InstallationID: "installation-1",
		InstallationStateRevision: 0, InstallationAuthorityRevision: 0, InstallationAuthorityDigest: strings.Repeat("c", 64),
		Provider: "github", ProviderInstanceKey: "github.com", RepositoryID: "repository-1",
		ProviderRepositoryID: "98765", ExternalRequestID: "42", ExpectedHeadSHA: strings.Repeat("b", 40),
		ContinuationIndex: 5, RemainingCommandBudget: 40,
		IdempotencyKey: "review-feedback-continuation-" + strings.Repeat("b", 64),
		LeaseID:        job.LeaseID, FencingToken: job.FencingToken, LeaseExpiresAt: job.LeaseExpiresAt,
		DeadlineAt: time.Date(2030, 8, 22, 12, 55, 0, 0, time.UTC),
		Feedback: v1.ReviewFeedbackContinuationFeedback{
			Trust: v1.ReviewFeedbackContinuationUntrustedText, Authority: v1.ReviewFeedbackContinuationNonAuthoritative,
			ContentEncoding: "utf-8", ContentFormat: "plain_text", UnicodeNormalization: "NFC", LineEndings: "LF",
			ContentDigest:    "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527",
			ContentByteCount: 25, Content: "Café\nFix the retry path.",
		},
	}
	authority.ApprovedScopeDigest = v1.ReviewFeedbackContinuationJobScopeDigest(job)
	return job, authority
}

func TestReviewFeedbackContinuationCommandAcceptsFrozenAuthorityAndScope(t *testing.T) {
	job, authority := validReviewFeedbackContinuationCommand()
	if authority.ApprovedScopeDigest != "9765d535c79c068bda783a28eb81e4f60e0cfee1a8fc799c5b918dc520b9b8be" {
		t.Fatalf("scope vector drifted: %s", authority.ApprovedScopeDigest)
	}
	if err := ReviewFeedbackContinuationCommand(job, authority); err != nil {
		t.Fatal(err)
	}
}

func TestReviewFeedbackContinuationCommandRejectsAuthorityAndTargetDrift(t *testing.T) {
	validJob, validAuthority := validReviewFeedbackContinuationCommand()
	tests := map[string]func(*v1.JobSpec, *v1.ReviewFeedbackContinuationAuthority){
		"legacy comment": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.ReviewComment = "trusted" },
		"plan job":       func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.Kind = v1.JobKindPlan },
		"project":        func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.ProjectID = "project-2" },
		"workflow job id": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) {
			job.JobID = "different-workflow-job"
		},
		"authority workflow job id": func(_ *v1.JobSpec, authority *v1.ReviewFeedbackContinuationAuthority) {
			authority.WorkflowRunID = "different-workflow-job"
		},
		"workflow":     func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.WorkflowRevision++ },
		"continuation": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.ContinuationCount-- },
		"source attempt reuse": func(job *v1.JobSpec, authority *v1.ReviewFeedbackContinuationAuthority) {
			authority.SourceAttemptID = job.AttemptID
		},
		"source manifest reuse": func(job *v1.JobSpec, authority *v1.ReviewFeedbackContinuationAuthority) {
			authority.SourceExecutionManifestID = job.ManifestID
		},
		"lease": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.LeaseID = "other-lease" },
		"fence": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.FencingToken++ },
		"repository absent": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) {
			job.Repositories[0].RepositoryID = "repository-2"
		},
		"ticket scope": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) {
			job.Ticket.Summary = "Changed trusted scope"
		},
		"checks scope": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) {
			job.RequiredChecks = []string{"security"}
		},
		"push scope":  func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.PushBranch = true },
		"title scope": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.PRTitle = "Changed title" },
		"capacity scope": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) {
			job.ProviderCapacityResumePolicy = v1.CapacityResumeAutomatic
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			job := validJob
			job.Repositories = append([]v1.JobRepository(nil), validJob.Repositories...)
			job.RequiredChecks = append([]string(nil), validJob.RequiredChecks...)
			authority := validAuthority
			mutate(&job, &authority)
			if ReviewFeedbackContinuationCommand(job, authority) == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestReviewFeedbackContinuationCommandRejectsForgedFeedbackAndMetadata(t *testing.T) {
	validJob, validAuthority := validReviewFeedbackContinuationCommand()
	tests := map[string]func(*v1.ReviewFeedbackContinuationAuthority){
		"derived event": func(v *v1.ReviewFeedbackContinuationAuthority) {
			v.ProviderReviewEventID = "derived-" + strings.Repeat("1", 64)
		},
		"username account": func(v *v1.ReviewFeedbackContinuationAuthority) { v.ProviderAccountID = "octocat" },
		"wrong trust":      func(v *v1.ReviewFeedbackContinuationAuthority) { v.Feedback.Trust = "trusted_instruction" },
		"approval":         func(v *v1.ReviewFeedbackContinuationAuthority) { v.Feedback.Authority = "approval" },
		"decomposed": func(v *v1.ReviewFeedbackContinuationAuthority) {
			v.Feedback.Content = "Cafe\u0301\nFix the retry path."
		},
		"content digest": func(v *v1.ReviewFeedbackContinuationAuthority) { v.Feedback.ContentDigest = strings.Repeat("0", 64) },
		"outer digest":   func(v *v1.ReviewFeedbackContinuationAuthority) { v.ContentDigest = strings.Repeat("0", 64) },
		"event state":    func(v *v1.ReviewFeedbackContinuationAuthority) { v.ReviewState = v1.ProviderReviewObservationApproved },
		"event kind": func(v *v1.ReviewFeedbackContinuationAuthority) {
			v.ReviewEventKind = v1.ProviderReviewObservationApproval
		},
		"workflow jump":          func(v *v1.ReviewFeedbackContinuationAuthority) { v.ResultingWorkflowRevision++ },
		"zero event revision":    func(v *v1.ReviewFeedbackContinuationAuthority) { v.ProviderReviewEventRevision = 0 },
		"zero delivery revision": func(v *v1.ReviewFeedbackContinuationAuthority) { v.AssignmentDeliveryStateRevision = 0 },
		"zero budget":            func(v *v1.ReviewFeedbackContinuationAuthority) { v.RemainingCommandBudget = 0 },
		"late evidence": func(v *v1.ReviewFeedbackContinuationAuthority) {
			v.EvidenceObservedAt = v.FeedbackContentObservedAt.Add(time.Minute)
		},
		"late deadline": func(v *v1.ReviewFeedbackContinuationAuthority) { v.DeadlineAt = v.LeaseExpiresAt.Add(time.Second) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			authority := validAuthority
			mutate(&authority)
			if ReviewFeedbackContinuationCommand(validJob, authority) == nil {
				t.Fatal("forgery accepted")
			}
		})
	}
}

func TestReviewFeedbackContinuationCommandRejectsNonGitHubProviders(t *testing.T) {
	validJob, validAuthority := validReviewFeedbackContinuationCommand()
	for name, mutate := range map[string]func(*v1.ReviewFeedbackContinuationAuthority){
		"gitlab": func(value *v1.ReviewFeedbackContinuationAuthority) {
			value.Provider = "gitlab"
			value.ProviderInstanceKey = "gitlab.com"
		},
		"bitbucket": func(value *v1.ReviewFeedbackContinuationAuthority) {
			value.Provider = "bitbucket"
			value.ProviderInstanceKey = "bitbucket.org"
			value.ProviderAccountID = "bb-account-42"
			value.ProviderRepositoryID = "{223e4567-e89b-42d3-a456-426614174000}"
			value.ProviderRepositoryPath = "workspace/repository"
		},
	} {
		t.Run(name, func(t *testing.T) {
			authority := validAuthority
			mutate(&authority)
			if ReviewFeedbackContinuationCommand(validJob, authority) == nil {
				t.Fatal("non-GitHub feedback continuation accepted")
			}
		})
	}
}

func TestReviewFeedbackContinuationCommandRejectsMalformedAuthorityClasses(t *testing.T) {
	validJob, validAuthority := validReviewFeedbackContinuationCommand()
	tests := map[string]func(*v1.ReviewFeedbackContinuationAuthority){
		"continuation id":      func(v *v1.ReviewFeedbackContinuationAuthority) { v.ReviewFeedbackContinuationID = "bad" },
		"opaque id":            func(v *v1.ReviewFeedbackContinuationAuthority) { v.OrganizationID = "../other" },
		"content operation id": func(v *v1.ReviewFeedbackContinuationAuthority) { v.ProviderReviewFeedbackContentOperationID = "bad" },
		"digest":               func(v *v1.ReviewFeedbackContinuationAuthority) { v.SourceExecutionManifestDigest = "bad" },
		"state overflow":       func(v *v1.ReviewFeedbackContinuationAuthority) { v.InstallationStateRevision = maxJSONSafeInteger + 1 },
		"provider":             func(v *v1.ReviewFeedbackContinuationAuthority) { v.Provider = "other" },
		"short bitbucket account": func(v *v1.ReviewFeedbackContinuationAuthority) {
			v.Provider = "bitbucket"
			v.ProviderInstanceKey = "bitbucket.org"
			v.ProviderAccountID = "a"
			v.ProviderRepositoryID = "{223e4567-e89b-42d3-a456-426614174000}"
			v.ProviderRepositoryPath = "workspace/repository"
		},
		"provider instance": func(v *v1.ReviewFeedbackContinuationAuthority) { v.ProviderInstanceKey = "GitHub.com" },
		"repository":        func(v *v1.ReviewFeedbackContinuationAuthority) { v.ProviderRepositoryID = "098765" },
		"request":           func(v *v1.ReviewFeedbackContinuationAuthority) { v.ExternalRequestID = "request-42" },
		"head":              func(v *v1.ReviewFeedbackContinuationAuthority) { v.ExpectedHeadSHA = "short" },
		"content bound": func(v *v1.ReviewFeedbackContinuationAuthority) {
			v.ContentByteCount = v1.ReviewFeedbackContinuationMaxContentBytes + 1
		},
		"encoding":  func(v *v1.ReviewFeedbackContinuationAuthority) { v.Feedback.ContentEncoding = "base64" },
		"zero time": func(v *v1.ReviewFeedbackContinuationAuthority) { v.ReviewEventOccurredAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			authority := validAuthority
			mutate(&authority)
			if ReviewFeedbackContinuationCommand(validJob, authority) == nil {
				t.Fatal("malformed authority accepted")
			}
		})
	}
}

func TestReviewFeedbackContinuationProviderInstanceMatchesFrozenDNSAuthorityContract(t *testing.T) {
	job, authority := validReviewFeedbackContinuationCommand()
	authority.ProviderInstanceKey = strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + ".example.com"
	if err := ReviewFeedbackContinuationCommand(job, authority); err != nil {
		t.Fatalf("valid app-protocol DNS authority rejected: %v", err)
	}
	authority.ProviderInstanceKey = "git..example.com"
	if ReviewFeedbackContinuationCommand(job, authority) == nil {
		t.Fatal("noncanonical empty DNS label accepted")
	}
}
