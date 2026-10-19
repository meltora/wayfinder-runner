package validation

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var humanChangeRequestIDPattern = regexp.MustCompile(`^human-change-request-[0-9a-f]{64}$`)

var humanChangeRequestProviders = map[string]bool{"github": true, "gitlab": true, "bitbucket": true}

func humanChangeRequestContinuationAuthority(value v1.ReviewFeedbackContinuationAuthority) error {
	if value.SchemaVersion != v1.ReviewFeedbackContinuationAuthorityVersion ||
		value.Purpose != v1.ReviewFeedbackContinuationPurpose ||
		!reviewFeedbackContinuationIDPattern.MatchString(value.ReviewFeedbackContinuationID) ||
		!reviewFeedbackContinuationIDPattern.MatchString(value.IdempotencyKey) ||
		!humanChangeRequestIDPattern.MatchString(value.HumanChangeRequestID) {
		return fmt.Errorf("change request continuation authority is invalid")
	}
	if err := humanChangeRequestCarriesNoProviderEvidence(value); err != nil {
		return err
	}
	for field, id := range map[string]string{
		"organization_id": value.OrganizationID, "project_id": value.ProjectID,
		"workflow_run_id": value.WorkflowRunID, "source_attempt_id": value.SourceAttemptID,
		"source_execution_manifest_id": value.SourceExecutionManifestID,
		"user_id":                      value.UserID, "organization_membership_id": value.OrganizationMembershipID,
		"installation_id": value.InstallationID, "repository_id": value.RepositoryID, "lease_id": value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	for field, digest := range map[string]string{
		"source_execution_manifest_digest": value.SourceExecutionManifestDigest,
		"content_digest":                   value.ContentDigest,
		"approved_scope_digest":            value.ApprovedScopeDigest,
	} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if value.ResultingWorkflowRevision == 0 || value.ResultingWorkflowRevision > maxJSONSafeInteger ||
		value.ResultingWorkflowRevision != value.SourceWorkflowRevision+1 ||
		value.ContinuationIndex == 0 || value.ContinuationIndex > v1.ReviewFeedbackContinuationMaxIndex ||
		value.RemainingCommandBudget == 0 || value.RemainingCommandBudget > v1.ReviewFeedbackContinuationMaxCommandBudget ||
		value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("change request continuation revision or bound is invalid")
	}
	if !humanChangeRequestProviders[value.Provider] {
		return fmt.Errorf("change request continuation provider is unsupported")
	}
	if err := canonicalPositiveDecimal("external_request_id", value.ExternalRequestID); err != nil {
		return err
	}
	if err := GitCommitSHA("expected_head_sha", value.ExpectedHeadSHA); err != nil {
		return err
	}
	if value.HeadBranch != "" {
		if err := humanChangeRequestHeadBranch(value.WorkflowRunID, value.HeadBranch); err != nil {
			return err
		}
	}
	if value.ContentByteCount == 0 || value.ContentByteCount > v1.ReviewFeedbackContinuationMaxContentBytes ||
		value.Feedback.ContentDigest != value.ContentDigest ||
		value.Feedback.ContentByteCount != value.ContentByteCount {
		return fmt.Errorf("change request content binding is invalid")
	}
	if value.Feedback.Trust != v1.HumanChangeRequestUntrustedText ||
		value.Feedback.Authority != v1.HumanChangeRequestAuthorized ||
		value.Feedback.ContentEncoding != "utf-8" || value.Feedback.ContentFormat != "plain_text" ||
		value.Feedback.UnicodeNormalization != "NFC" || value.Feedback.LineEndings != "LF" {
		return fmt.Errorf("change request feedback labels are invalid")
	}
	if err := ProviderReviewFeedbackContentNormalizedText(value.Feedback.Content, value.Feedback.ContentDigest, value.Feedback.ContentByteCount); err != nil {
		return err
	}
	if value.DeadlineAt.IsZero() || value.LeaseExpiresAt.IsZero() || !value.DeadlineAt.Before(value.LeaseExpiresAt) {
		return fmt.Errorf("change request continuation timing is invalid")
	}
	return nil
}

func humanChangeRequestHeadBranch(workflowRunID, branch string) error {
	if len(branch) > 300 || !strings.HasPrefix(branch, "job/"+workflowRunID+"/") {
		return fmt.Errorf("change request head branch must belong to the workflow")
	}
	return GitRef("head_branch", branch)
}

func humanChangeRequestCarriesNoProviderEvidence(value v1.ReviewFeedbackContinuationAuthority) error {
	var zero time.Time
	if value.AssignmentDeliveryID != "" || value.AssignmentDeliveryStateRevision != 0 ||
		value.AssignmentObservationID != "" || value.ObservedReviewerID != "" ||
		value.ProviderReviewObservationID != "" || value.ProviderReviewObservationResultPayloadChecksum != "" ||
		value.ProviderReviewEventID != "" || value.ProviderReviewEventRevision != 0 ||
		value.ReviewEventKind != "" || value.ReviewState != "" ||
		value.ReviewEventOccurredAt != zero || value.ProviderReviewEvidenceDigest != "" ||
		value.EvidenceObservedAt != zero || value.ProviderReviewFeedbackContentOperationID != "" ||
		value.ProviderReviewFeedbackContentResultPayloadChecksum != "" || value.FeedbackContentRequestDigest != "" ||
		value.FeedbackContentObservedAt != zero || value.ProviderAccountID != "" ||
		value.ReviewerIdentityID != "" || value.InstallationStateRevision != 0 ||
		value.InstallationAuthorityRevision != 0 || value.InstallationAuthorityDigest != "" ||
		value.ProviderInstanceKey != "" || value.ProviderRepositoryID != "" || value.ProviderRepositoryPath != "" {
		return fmt.Errorf("change request continuation must not carry provider review evidence")
	}
	return nil
}

func postMergeFixBase(job v1.JobSpec, authority v1.ReviewFeedbackContinuationAuthority) error {
	if !authority.IsHumanChangeRequest() || authority.HeadBranch != "" {
		return nil
	}
	for _, repository := range job.Repositories {
		if repository.RepositoryID == authority.RepositoryID && repository.BaseSHA == authority.ExpectedHeadSHA {
			return nil
		}
	}
	return fmt.Errorf("post-merge fix must start from the merge commit")
}
