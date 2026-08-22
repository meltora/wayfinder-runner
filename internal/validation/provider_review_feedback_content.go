package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"golang.org/x/text/unicode/norm"
)

var (
	providerReviewFeedbackContentOperationIDPattern = regexp.MustCompile(`^provider-review-feedback-content-operation-[0-9a-f]{64}$`)
	providerReviewFeedbackContentCommandIDPattern   = regexp.MustCompile(`^provider-review-feedback-content-command-[0-9a-f]{64}$`)
	providerReviewFeedbackContentIdempotencyPattern = regexp.MustCompile(`^provider-review-feedback-content-[0-9a-f]{64}$`)
)

func providerReviewFeedbackContentAuthority(v v1.ProviderReviewFeedbackContentAuthority) error {
	if v.SchemaVersion != v1.ProviderReviewFeedbackContentAuthorityVersion || !providerReviewFeedbackContentOperationIDPattern.MatchString(v.ProviderReviewFeedbackContentOperationID) {
		return fmt.Errorf("provider review feedback content authority is invalid")
	}
	for field, id := range map[string]string{
		"organization_id": v.OrganizationID, "project_id": v.ProjectID, "workflow_run_id": v.WorkflowRunID,
		"assignment_delivery_id": v.AssignmentDeliveryID, "assignment_observation_id": v.AssignmentObservationID,
		"observed_reviewer_id": v.ObservedReviewerID, "provider_review_observation_id": v.ProviderReviewObservationID,
		"user_id": v.UserID, "organization_membership_id": v.OrganizationMembershipID,
		"reviewer_identity_id": v.ReviewerIdentityID, "identity_verification_id": v.IdentityVerificationID,
		"provider_account_proof_id": v.ProviderAccountProofID, "repository_id": v.RepositoryID,
		"installation_id": v.InstallationID, "lease_id": v.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	for field, digest := range map[string]string{
		"provider_review_observation_result_payload_checksum": v.ProviderReviewObservationResultPayloadChecksum,
		"content_digest": v.ContentDigest, "code_review_authority_digest": v.CodeReviewAuthorityDigest,
		"identity_verification_digest": v.IdentityVerificationDigest, "provider_account_proof_digest": v.ProviderAccountProofDigest,
		"installation_authority_digest": v.InstallationAuthorityDigest, "feedback_content_request_digest": v.FeedbackContentRequestDigest,
		"operation_digest": v.OperationDigest, "command_digest": v.CommandDigest,
	} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	positive := []uint64{v.AssignmentDeliveryStateRevision, v.ProviderReviewEventRevision, v.CodeReviewAuthorityRevision, v.IdentityRevision, v.ProviderAccountProofRevision, v.FencingToken}
	for _, revision := range positive {
		if revision == 0 || revision > maxJSONSafeInteger {
			return fmt.Errorf("positive revision is invalid")
		}
	}
	for _, revision := range []uint64{v.WorkflowStateRevision, v.OrganizationMembershipStateRevision, v.IdentityStateRevision, v.ProviderAccountProofStateRevision, v.InstallationStateRevision, v.InstallationAuthorityRevision} {
		if revision > maxJSONSafeInteger {
			return fmt.Errorf("state revision is invalid")
		}
	}
	if v.Purpose != "review_feedback_continuation" || v.ContentSource != "provider_review_event" || v.ReviewEventKind != v1.ProviderReviewObservationChangeRequest || v.ReviewState != v1.ProviderReviewObservationChangesRequested || !metadataID(v.ProviderReviewEventID) {
		return fmt.Errorf("feedback evidence metadata is invalid")
	}
	if v.EvidenceObservedAt.IsZero() || v.ReviewEventOccurredAt.IsZero() || v.ReviewEventOccurredAt.After(v.EvidenceObservedAt) || v.LeaseExpiresAt.IsZero() || v.EvidenceObservedAt.After(v.LeaseExpiresAt) {
		return fmt.Errorf("feedback evidence timing is invalid")
	}
	if err := providerReviewerAssignmentRepository(v1.ProviderReviewerAssignmentOperationAuthority{Provider: v.Provider, ProviderInstanceKey: v.ProviderInstanceKey, ProviderRepositoryID: v.ProviderRepositoryID, ProviderRepositoryPath: v.ProviderRepositoryPath}); err != nil {
		return err
	}
	if err := reviewerProviderAccountID(v.Provider, v.ProviderAccountID); err != nil {
		return err
	}
	if err := canonicalPositiveDecimal("external_request_id", v.ExternalRequestID); err != nil {
		return err
	}
	if err := canonicalReviewEventID(v); err != nil {
		return err
	}
	if err := GitCommitSHA("expected_head_sha", v.ExpectedHeadSHA); err != nil {
		return err
	}
	if v.ContentByteCount == 0 || v.ContentByteCount > v1.ProviderReviewFeedbackContentMaxBytes || !providerReviewFeedbackContentIdempotencyPattern.MatchString(v.IdempotencyKey) {
		return fmt.Errorf("feedback content bound is invalid")
	}
	if v.IdentityVerificationExpiresAt.IsZero() || v.ProviderAccountProofExpiresAt.IsZero() || v.IdentityVerificationExpiresAt.After(v.ProviderAccountProofExpiresAt) || v.LeaseExpiresAt.After(v.IdentityVerificationExpiresAt) || v.LeaseExpiresAt.After(v.ProviderAccountProofExpiresAt) {
		return fmt.Errorf("identity authority expiry is invalid")
	}
	if v.CodeReviewAuthorityExpiresAt != nil && (v.CodeReviewAuthorityExpiresAt.IsZero() || v.LeaseExpiresAt.After(*v.CodeReviewAuthorityExpiresAt)) {
		return fmt.Errorf("code review authority expiry is invalid")
	}
	return nil
}

func canonicalPositiveDecimal(field, value string) error {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != value {
		return fmt.Errorf("%s is invalid", field)
	}
	return nil
}

func canonicalReviewEventID(v v1.ProviderReviewFeedbackContentAuthority) error {
	return canonicalPositiveDecimal("provider_review_event_id", v.ProviderReviewEventID)
}

func ProviderReviewFeedbackContentOperation(v v1.ProviderReviewFeedbackContentOperation) error {
	return providerReviewFeedbackContentAuthority(v.ProviderReviewFeedbackContentAuthority)
}

func ProviderReviewFeedbackContentCommandMessageID(v string) error {
	if !providerReviewFeedbackContentCommandIDPattern.MatchString(v) {
		return fmt.Errorf("command message ID is invalid")
	}
	return nil
}

func ProviderReviewFeedbackContentResultEvent(e v1.ProviderReviewFeedbackContentResultEvent) error {
	if e.Kind != v1.EventProviderReviewFeedbackContentResult {
		return fmt.Errorf("result kind is invalid")
	}
	if err := providerReviewFeedbackContentAuthority(e.ProviderReviewFeedbackContentAuthority); err != nil {
		return err
	}
	if err := ProviderReviewFeedbackContentCommandMessageID(e.CommandMessageID); err != nil {
		return err
	}
	if e.ObservedAt.IsZero() || e.ObservedAt.Before(e.EvidenceObservedAt) || e.ObservedAt.After(e.LeaseExpiresAt) {
		return fmt.Errorf("result observation time is invalid")
	}
	o := e.Outcome
	switch o.Kind {
	case v1.ProviderReviewFeedbackContentSucceeded:
		if o.RequestState != v1.ProviderReviewObservationRequestOpen || o.ObservedHeadSHA != e.ExpectedHeadSHA || o.ContentEncoding != "utf-8" || o.ContentFormat != "plain_text" || o.UnicodeNormalization != "NFC" || o.LineEndings != "LF" || o.Code != "" || o.RetryAfterMS != nil {
			return fmt.Errorf("success outcome is invalid")
		}
		if err := normalizedFeedbackContent(o.Content, o.ContentDigest, o.ContentByteCount); err != nil {
			return err
		}
		if o.ContentDigest != e.ContentDigest || o.ContentByteCount != e.ContentByteCount {
			return fmt.Errorf("content metadata changed")
		}
	case v1.ProviderReviewFeedbackContentRetryable:
		if hasFeedbackSuccessFields(o) || (o.Code != v1.ProviderReviewFeedbackContentProviderUnavailable && o.Code != v1.ProviderReviewFeedbackContentRateLimited) || o.RetryAfterMS != nil && *o.RetryAfterMS > maxSourceControlRetryAfterMS {
			return fmt.Errorf("retryable outcome is invalid")
		}
	case v1.ProviderReviewFeedbackContentPermanent:
		if hasFeedbackSuccessFields(o) || o.RetryAfterMS != nil || !providerReviewFeedbackContentPermanent(o.Code) {
			return fmt.Errorf("permanent outcome is invalid")
		}
	default:
		return fmt.Errorf("outcome kind is invalid")
	}
	return nil
}

func hasFeedbackSuccessFields(o v1.ProviderReviewFeedbackContentResultOutcome) bool {
	return o.RequestState != "" || o.ObservedHeadSHA != "" || o.ContentEncoding != "" || o.ContentFormat != "" || o.UnicodeNormalization != "" || o.LineEndings != "" || o.ContentDigest != "" || o.ContentByteCount != 0 || o.Content != ""
}

func normalizedFeedbackContent(content, digest string, count uint64) error {
	if !utf8.ValidString(content) || !norm.NFC.IsNormalString(content) || uint64(len([]byte(content))) != count || count > v1.ProviderReviewFeedbackContentMaxBytes {
		return fmt.Errorf("content is not normalized plain text")
	}
	for _, r := range content {
		if r == '\r' || r <= 0x08 || r == 0x0b || r == 0x0c || r >= 0x0e && r <= 0x1f || r == 0x7f {
			return fmt.Errorf("content is not safe plain text")
		}
	}
	sum := sha256.Sum256([]byte(content))
	if hex.EncodeToString(sum[:]) != digest {
		return fmt.Errorf("content digest mismatch")
	}
	return nil
}

// ProviderReviewFeedbackContentNormalizedText validates the provider body at the
// trust boundary before it can be returned to the control plane.
func ProviderReviewFeedbackContentNormalizedText(content, digest string, count uint64) error {
	return normalizedFeedbackContent(content, digest, count)
}

func providerReviewFeedbackContentPermanent(code v1.ProviderReviewFeedbackContentErrorCode) bool {
	switch code {
	case v1.ProviderReviewFeedbackContentRequestNotFound, v1.ProviderReviewFeedbackContentRequestNotOpen, v1.ProviderReviewFeedbackContentRepositoryMismatch, v1.ProviderReviewFeedbackContentHeadMismatch, v1.ProviderReviewFeedbackContentReviewEventNotFound, v1.ProviderReviewFeedbackContentReviewEventMetadataChanged, v1.ProviderReviewFeedbackContentInstallationInactive, v1.ProviderReviewFeedbackContentInstallationAuthorityChanged, v1.ProviderReviewFeedbackContentInstallationPermissionDenied, v1.ProviderReviewFeedbackContentReviewerIdentityChanged, v1.ProviderReviewFeedbackContentReviewerIdentityExpired, v1.ProviderReviewFeedbackContentProviderAccountProofChanged, v1.ProviderReviewFeedbackContentProviderAccountProofExpired, v1.ProviderReviewFeedbackContentMembershipAuthorityChanged, v1.ProviderReviewFeedbackContentCodeReviewAuthorityChanged, v1.ProviderReviewFeedbackContentUnsupportedProviderInstance, v1.ProviderReviewFeedbackContentTooLarge, v1.ProviderReviewFeedbackContentInvalidPlainText, v1.ProviderReviewFeedbackContentInvalidProviderResponse:
		return true
	}
	return false
}
