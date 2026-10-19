package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validHumanChangeRequestCommand() (v1.JobSpec, v1.ReviewFeedbackContinuationAuthority) {
	job, provider := validReviewFeedbackContinuationCommand()
	authority := v1.ReviewFeedbackContinuationAuthority{
		SchemaVersion: 1, Purpose: v1.ReviewFeedbackContinuationPurpose,
		ReviewFeedbackContinuationID: provider.ReviewFeedbackContinuationID,
		OrganizationID:               "organization-1", ProjectID: job.ProjectID, WorkflowRunID: job.JobID,
		SourceWorkflowRevision: 11, ResultingWorkflowRevision: 12,
		SourceAttemptID: "source-attempt-1", SourceExecutionManifestID: "execution-manifest-1",
		SourceExecutionManifestDigest: strings.Repeat("2", 64),
		ContentSource:                 v1.HumanChangeRequestContentSource,
		HumanChangeRequestID:          "human-change-request-" + strings.Repeat("d", 64),
		UserID:                        "user-1", OrganizationMembershipID: "membership-1",
		InstallationID: "installation-1", Provider: "github", RepositoryID: "repository-1",
		ExternalRequestID: "2", HeadBranch: "job/workflow-1/source-attempt-1",
		ExpectedHeadSHA: strings.Repeat("b", 40), ApprovedScopeDigest: provider.ApprovedScopeDigest,
		ContinuationIndex: 5, RemainingCommandBudget: 40, IdempotencyKey: provider.IdempotencyKey,
		LeaseID: job.LeaseID, FencingToken: job.FencingToken, LeaseExpiresAt: job.LeaseExpiresAt,
		DeadlineAt:    job.LeaseExpiresAt.Add(-5 * time.Minute),
		ContentDigest: provider.ContentDigest, ContentByteCount: provider.ContentByteCount,
		Feedback: v1.ReviewFeedbackContinuationFeedback{
			Trust: v1.HumanChangeRequestUntrustedText, Authority: v1.HumanChangeRequestAuthorized,
			ContentEncoding: "utf-8", ContentFormat: "plain_text", UnicodeNormalization: "NFC", LineEndings: "LF",
			ContentDigest: provider.Feedback.ContentDigest, ContentByteCount: provider.Feedback.ContentByteCount,
			Content: provider.Feedback.Content,
		},
	}
	return job, authority
}

func TestHumanChangeRequestContinuationAcceptsMemberAuthorityWithoutProviderEvidence(t *testing.T) {
	job, authority := validHumanChangeRequestCommand()
	if err := ReviewFeedbackContinuationCommand(job, authority); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"gitlab", "bitbucket"} {
		candidate := authority
		candidate.Provider = provider
		if err := ReviewFeedbackContinuationCommand(job, candidate); err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
	}
}

func TestHumanChangeRequestContinuationRejectsForgedAuthority(t *testing.T) {
	validJob, validAuthority := validHumanChangeRequestCommand()
	tests := map[string]func(*v1.JobSpec, *v1.ReviewFeedbackContinuationAuthority){
		"request id": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.HumanChangeRequestID = "change-1" },
		"foreign branch": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.HeadBranch = "job/workflow-2/attempt"
		},
		"unsafe branch":  func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.HeadBranch = "job/workflow-1/a..b" },
		"default branch": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.HeadBranch = "main" },
		"missing branch": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.HeadBranch = "" },
		"short head":     func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.ExpectedHeadSHA = "abc" },
		"provider labels": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.Feedback.Trust = v1.ReviewFeedbackContinuationUntrustedText
		},
		"authority label": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.Feedback.Authority = "approved" },
		"content digest": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.ContentDigest = strings.Repeat("e", 64)
		},
		"content tamper": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.Feedback.Content = "Cafe\nFix the retry path."
		},
		"unknown provider":  func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.Provider = "gitea" },
		"revision skip":     func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.ResultingWorkflowRevision++ },
		"deadline":          func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.DeadlineAt = v.LeaseExpiresAt },
		"missing user":      func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.UserID = "" },
		"budget":            func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.RemainingCommandBudget = 0 },
		"scope":             func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.Ticket.Summary = "Changed" },
		"lease":             func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) { job.LeaseID = "other-lease" },
		"provider event":    func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.ProviderReviewEventID = "7001" },
		"reviewer identity": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) { v.ReviewerIdentityID = "reviewer-1" },
		"installation proof": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.InstallationAuthorityDigest = strings.Repeat("c", 64)
		},
		"observed time": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.EvidenceObservedAt = time.Date(2030, 8, 22, 12, 0, 0, 0, time.UTC)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			job := validJob
			job.Repositories = append([]v1.JobRepository(nil), validJob.Repositories...)
			authority := validAuthority
			mutate(&job, &authority)
			if ReviewFeedbackContinuationCommand(job, authority) == nil {
				t.Fatal("forged change request accepted")
			}
		})
	}
}

func TestProviderContinuationRejectsChangeRequestIdentity(t *testing.T) {
	job, authority := validReviewFeedbackContinuationCommand()
	authority.HumanChangeRequestID = "human-change-request-" + strings.Repeat("d", 64)
	if ReviewFeedbackContinuationCommand(job, authority) == nil {
		t.Fatal("provider continuation accepted a change request identity")
	}
}

func TestProviderContinuationAcceptsOnlyTheWorkflowPullRequestBranch(t *testing.T) {
	job, authority := validReviewFeedbackContinuationCommand()
	authority.HeadBranch = "job/workflow-1/source-attempt-1"
	if err := ReviewFeedbackContinuationCommand(job, authority); err != nil {
		t.Fatalf("provider continuation on the existing branch rejected: %v", err)
	}
	for name, branch := range map[string]string{
		"foreign workflow": "job/workflow-2/attempt",
		"unsafe branch":    "job/workflow-1/a..b",
		"default branch":   "main",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := authority
			candidate.HeadBranch = branch
			if ReviewFeedbackContinuationCommand(job, candidate) == nil {
				t.Fatal("provider continuation accepted an unsafe head branch")
			}
		})
	}
}

func validPostMergeFixCommand() (v1.JobSpec, v1.ReviewFeedbackContinuationAuthority) {
	job, authority := validHumanChangeRequestCommand()
	job.Repositories = append([]v1.JobRepository(nil), job.Repositories...)
	job.Repositories[0].BaseSHA = strings.Repeat("f", 40)
	authority.HeadBranch = ""
	authority.ExpectedHeadSHA = strings.Repeat("f", 40)
	return job, authority
}

func TestHumanChangeRequestContinuationAcceptsAPostMergeFixFromTheMergeCommit(t *testing.T) {
	job, authority := validPostMergeFixCommand()
	if err := ReviewFeedbackContinuationCommand(job, authority); err != nil {
		t.Fatalf("post-merge fix rejected: %v", err)
	}
}

func TestHumanChangeRequestContinuationRejectsAPostMergeFixAwayFromTheMergeCommit(t *testing.T) {
	validJob, validAuthority := validPostMergeFixCommand()
	tests := map[string]func(*v1.JobSpec, *v1.ReviewFeedbackContinuationAuthority){
		"base differs from merge commit": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) {
			job.Repositories[0].BaseSHA = strings.Repeat("a", 40)
		},
		"missing base": func(job *v1.JobSpec, _ *v1.ReviewFeedbackContinuationAuthority) {
			job.Repositories[0].BaseSHA = ""
		},
		"merge commit not a commit": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.ExpectedHeadSHA = "main"
		},
		"repository outside job": func(_ *v1.JobSpec, v *v1.ReviewFeedbackContinuationAuthority) {
			v.RepositoryID = "repository-2"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			job := validJob
			job.Repositories = append([]v1.JobRepository(nil), validJob.Repositories...)
			authority := validAuthority
			mutate(&job, &authority)
			if ReviewFeedbackContinuationCommand(job, authority) == nil {
				t.Fatal("post-merge fix away from the merge commit accepted")
			}
		})
	}
}
