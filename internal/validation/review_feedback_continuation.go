package validation

import (
	"fmt"
	"regexp"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var (
	reviewFeedbackContinuationIDPattern               = regexp.MustCompile(`^review-feedback-continuation-[0-9a-f]{64}$`)
	reviewFeedbackContinuationProviderInstancePattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?::[1-9][0-9]{0,4})?$`)
)

func reviewFeedbackContinuationAuthority(value v1.ReviewFeedbackContinuationAuthority) error {
	if value.IsHumanChangeRequest() {
		return humanChangeRequestContinuationAuthority(value)
	}
	if value.HumanChangeRequestID != "" {
		return fmt.Errorf("provider continuation must not carry change request fields")
	}
	if value.HeadBranch != "" {
		if err := humanChangeRequestHeadBranch(value.WorkflowRunID, value.HeadBranch); err != nil {
			return err
		}
	}
	if value.SchemaVersion != v1.ReviewFeedbackContinuationAuthorityVersion ||
		value.Purpose != v1.ReviewFeedbackContinuationPurpose ||
		!reviewFeedbackContinuationIDPattern.MatchString(value.ReviewFeedbackContinuationID) ||
		!reviewFeedbackContinuationIDPattern.MatchString(value.IdempotencyKey) {
		return fmt.Errorf("review feedback continuation authority is invalid")
	}
	for field, id := range map[string]string{
		"organization_id": value.OrganizationID, "project_id": value.ProjectID,
		"workflow_run_id": value.WorkflowRunID, "source_attempt_id": value.SourceAttemptID,
		"source_execution_manifest_id":   value.SourceExecutionManifestID,
		"assignment_delivery_id":         value.AssignmentDeliveryID,
		"assignment_observation_id":      value.AssignmentObservationID,
		"observed_reviewer_id":           value.ObservedReviewerID,
		"provider_review_observation_id": value.ProviderReviewObservationID,
		"user_id":                        value.UserID, "organization_membership_id": value.OrganizationMembershipID,
		"reviewer_identity_id": value.ReviewerIdentityID, "installation_id": value.InstallationID,
		"repository_id": value.RepositoryID, "lease_id": value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if !providerReviewFeedbackContentOperationIDPattern.MatchString(value.ProviderReviewFeedbackContentOperationID) {
		return fmt.Errorf("provider review feedback content operation ID is invalid")
	}
	for field, digest := range map[string]string{
		"source_execution_manifest_digest":                         value.SourceExecutionManifestDigest,
		"provider_review_observation_result_payload_checksum":      value.ProviderReviewObservationResultPayloadChecksum,
		"provider_review_evidence_digest":                          value.ProviderReviewEvidenceDigest,
		"provider_review_feedback_content_result_payload_checksum": value.ProviderReviewFeedbackContentResultPayloadChecksum,
		"feedback_content_request_digest":                          value.FeedbackContentRequestDigest,
		"content_digest":                                           value.ContentDigest,
		"installation_authority_digest":                            value.InstallationAuthorityDigest,
		"approved_scope_digest":                                    value.ApprovedScopeDigest,
	} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	for _, number := range []uint64{
		value.SourceWorkflowRevision, value.ResultingWorkflowRevision,
		value.AssignmentDeliveryStateRevision, value.ProviderReviewEventRevision,
		value.ContinuationIndex, value.RemainingCommandBudget, value.FencingToken,
	} {
		if number == 0 || number > maxJSONSafeInteger {
			return fmt.Errorf("positive continuation authority value is invalid")
		}
	}
	if value.InstallationStateRevision > maxJSONSafeInteger || value.InstallationAuthorityRevision > maxJSONSafeInteger ||
		value.ResultingWorkflowRevision != value.SourceWorkflowRevision+1 ||
		value.ContinuationIndex > v1.ReviewFeedbackContinuationMaxIndex ||
		value.RemainingCommandBudget > v1.ReviewFeedbackContinuationMaxCommandBudget {
		return fmt.Errorf("continuation revision or bound is invalid")
	}
	if value.ContentSource != v1.ReviewFeedbackContinuationContentSource ||
		value.ReviewEventKind != v1.ProviderReviewObservationChangeRequest ||
		value.ReviewState != v1.ProviderReviewObservationChangesRequested {
		return fmt.Errorf("continuation evidence kind is invalid")
	}
	if err := canonicalPositiveDecimal("provider_review_event_id", value.ProviderReviewEventID); err != nil {
		return err
	}
	// Protocol v1 only continues exact native GitHub change-request review bodies.
	// GitLab and Bitbucket reviewer-state events are derived metadata and are not
	// safe content authorities.
	if value.Provider != "github" {
		return fmt.Errorf("continuation provider is unsupported")
	}
	if err := reviewerProviderAccountID(value.Provider, value.ProviderAccountID); err != nil {
		return err
	}
	if len(value.ProviderInstanceKey) > 512 || !reviewFeedbackContinuationProviderInstancePattern.MatchString(value.ProviderInstanceKey) {
		return fmt.Errorf("continuation provider instance is invalid")
	}
	if err := providerReviewerAssignmentRepository(v1.ProviderReviewerAssignmentOperationAuthority{
		Provider: value.Provider, ProviderInstanceKey: value.ProviderInstanceKey,
		ProviderRepositoryID: value.ProviderRepositoryID, ProviderRepositoryPath: value.ProviderRepositoryPath,
	}); err != nil {
		return err
	}
	if err := canonicalPositiveDecimal("external_request_id", value.ExternalRequestID); err != nil {
		return err
	}
	if err := GitCommitSHA("expected_head_sha", value.ExpectedHeadSHA); err != nil {
		return err
	}
	if value.ContentByteCount == 0 || value.ContentByteCount > v1.ReviewFeedbackContinuationMaxContentBytes ||
		value.Feedback.ContentDigest != value.ContentDigest ||
		value.Feedback.ContentByteCount != value.ContentByteCount {
		return fmt.Errorf("continuation content binding is invalid")
	}
	if value.Feedback.Trust != v1.ReviewFeedbackContinuationUntrustedText ||
		value.Feedback.Authority != v1.ReviewFeedbackContinuationNonAuthoritative ||
		value.Feedback.ContentEncoding != "utf-8" || value.Feedback.ContentFormat != "plain_text" ||
		value.Feedback.UnicodeNormalization != "NFC" || value.Feedback.LineEndings != "LF" {
		return fmt.Errorf("continuation feedback labels are invalid")
	}
	if err := ProviderReviewFeedbackContentNormalizedText(value.Feedback.Content, value.Feedback.ContentDigest, value.Feedback.ContentByteCount); err != nil {
		return err
	}
	if !orderedContinuationTimes(value.ReviewEventOccurredAt, value.EvidenceObservedAt, value.FeedbackContentObservedAt, value.DeadlineAt, value.LeaseExpiresAt) {
		return fmt.Errorf("continuation evidence timing is invalid")
	}
	return nil
}

func orderedContinuationTimes(values ...time.Time) bool {
	var previous time.Time
	for _, value := range values {
		if value.IsZero() || !previous.IsZero() && value.Before(previous) {
			return false
		}
		previous = value
	}
	return true
}

func ReviewFeedbackContinuationCommand(job v1.JobSpec, authority v1.ReviewFeedbackContinuationAuthority) error {
	if err := JobSpec(job); err != nil {
		return err
	}
	if err := reviewFeedbackContinuationAuthority(authority); err != nil {
		return err
	}
	if job.Kind != v1.JobKindBuild || job.HasReviewCommentField() || job.JobID != authority.WorkflowRunID ||
		job.ProjectID != authority.ProjectID ||
		job.WorkflowRevision < 0 || uint64(job.WorkflowRevision) != authority.ResultingWorkflowRevision ||
		job.ContinuationCount < 0 || uint64(job.ContinuationCount) != authority.ContinuationIndex ||
		job.AttemptID == authority.SourceAttemptID || job.ManifestID == authority.SourceExecutionManifestID {
		return fmt.Errorf("continuation target job or source lineage is invalid")
	}
	repositoryMatches := false
	for _, repository := range job.Repositories {
		if repository.RepositoryID == authority.RepositoryID {
			repositoryMatches = true
			break
		}
	}
	if !repositoryMatches {
		return fmt.Errorf("continuation repository is outside the target job")
	}
	if err := postMergeFixBase(job, authority); err != nil {
		return err
	}
	if job.LeaseID != authority.LeaseID || job.FencingToken != authority.FencingToken ||
		!job.LeaseExpiresAt.Equal(authority.LeaseExpiresAt) {
		return fmt.Errorf("continuation target lease binding is invalid")
	}
	if got := v1.ReviewFeedbackContinuationJobScopeDigest(job); got == "" || got != authority.ApprovedScopeDigest {
		return fmt.Errorf("continuation approved scope digest mismatch")
	}
	return nil
}
