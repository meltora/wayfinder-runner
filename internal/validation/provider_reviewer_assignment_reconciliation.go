package validation

import (
	"fmt"
	"regexp"
	"strconv"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var (
	providerReviewerAssignmentReconciliationOperationIDPattern = regexp.MustCompile(`^provider-reviewer-assignment-reconciliation-operation-[0-9a-f]{64}$`)
	providerReviewerAssignmentReconciliationCommandIDPattern   = regexp.MustCompile(`^provider-reviewer-assignment-reconciliation-command-[0-9a-f]{64}$`)
	providerReviewerAssignmentReconciliationIdempotencyPattern = regexp.MustCompile(`^provider-reviewer-assignment-reconciliation-[0-9a-f]{64}$`)
)

func providerReviewerAssignmentReconciliationAuthority(value v1.ProviderReviewerAssignmentReconciliationOperationAuthority) error {
	if value.SchemaVersion != v1.ProviderReviewerAssignmentReconciliationAuthorityVersion ||
		!providerReviewerAssignmentReconciliationOperationIDPattern.MatchString(value.ProviderReviewerAssignmentReconciliationOperationID) {
		return fmt.Errorf("provider reviewer assignment reconciliation authority is invalid")
	}
	for field, id := range map[string]string{
		"organization_id": value.OrganizationID, "project_id": value.ProjectID, "workflow_run_id": value.WorkflowRunID,
		"assignment_plan_id": value.AssignmentPlanID, "source_publication_observation_id": value.SourcePublicationObservationID,
		"delivery_id": value.DeliveryID, "assignment_observation_id": value.AssignmentObservationID,
		"assignment_result_delivery_id": value.AssignmentResultDeliveryID, "repository_id": value.RepositoryID,
		"installation_id": value.InstallationID, "lease_id": value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if !providerReviewerAssignmentOperationIDPattern.MatchString(value.AssignmentOperationID) {
		return fmt.Errorf("assignment_operation_id is invalid")
	}
	for field, digest := range map[string]string{
		"assignment_plan_digest": value.AssignmentPlanDigest, "assignment_operation_digest": value.AssignmentOperationDigest,
		"assignment_command_digest": value.AssignmentCommandDigest, "assignment_result_payload_checksum": value.AssignmentResultPayloadChecksum,
		"installation_authority_digest": value.InstallationAuthorityDigest, "assignment_recipient_set_digest": value.AssignmentRecipientSetDigest,
		"intended_reviewer_set_digest": value.IntendedReviewerSetDigest, "operation_digest": value.OperationDigest, "command_digest": value.CommandDigest,
	} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if value.InstallationStateRevision > maxJSONSafeInteger || value.InstallationAuthorityRevision > maxJSONSafeInteger || value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("provider reviewer assignment reconciliation numeric authority is invalid")
	}
	if value.Provider != "github" && value.Provider != "gitlab" && value.Provider != "bitbucket" {
		return fmt.Errorf("provider is unsupported")
	}
	if len(value.ProviderInstanceKey) > 255 || !providerInstanceKeyPattern.MatchString(value.ProviderInstanceKey) || value.Provider == "bitbucket" && value.ProviderInstanceKey != "bitbucket.org" {
		return fmt.Errorf("provider_instance_key is invalid")
	}
	assignmentAuthority := v1.ProviderReviewerAssignmentOperationAuthority{Provider: value.Provider, ProviderInstanceKey: value.ProviderInstanceKey, ProviderRepositoryID: value.ProviderRepositoryID, ProviderRepositoryPath: value.ProviderRepositoryPath}
	if err := providerReviewerAssignmentRepository(assignmentAuthority); err != nil {
		return err
	}
	requestID, err := strconv.ParseUint(value.ExternalRequestID, 10, 64)
	if err != nil || requestID == 0 || strconv.FormatUint(requestID, 10) != value.ExternalRequestID {
		return fmt.Errorf("external_request_id is invalid")
	}
	if err := GitCommitSHA("expected_head_sha", value.ExpectedHeadSHA); err != nil {
		return err
	}
	if !providerReviewerAssignmentReconciliationIdempotencyPattern.MatchString(value.IdempotencyKey) {
		return fmt.Errorf("idempotency_key is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("lease_expires_at is required")
	}
	return nil
}

func ProviderReviewerAssignmentReconciliationOperation(value v1.ProviderReviewerAssignmentReconciliationOperation) error {
	if err := providerReviewerAssignmentReconciliationAuthority(value.ProviderReviewerAssignmentReconciliationOperationAuthority); err != nil {
		return err
	}
	if len(value.IntendedReviewers) == 0 || len(value.IntendedReviewers) > 100 {
		return fmt.Errorf("intended reviewers count is invalid")
	}
	identities := make(map[string]struct{}, len(value.IntendedReviewers))
	previous := ""
	for _, reviewer := range value.IntendedReviewers {
		if err := ID("reviewer_identity_id", reviewer.ReviewerIdentityID); err != nil {
			return err
		}
		if err := reviewerProviderAccountID(value.Provider, reviewer.ProviderAccountID); err != nil {
			return err
		}
		if previous != "" && reviewer.ProviderAccountID <= previous {
			return fmt.Errorf("intended reviewers must be sorted and unique")
		}
		if _, exists := identities[reviewer.ReviewerIdentityID]; exists {
			return fmt.Errorf("reviewer identity is duplicated")
		}
		previous = reviewer.ProviderAccountID
		identities[reviewer.ReviewerIdentityID] = struct{}{}
	}
	if v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(value.IntendedReviewers) != value.IntendedReviewerSetDigest {
		return fmt.Errorf("intended reviewer set digest does not bind reviewers")
	}
	return nil
}

func ProviderReviewerAssignmentReconciliationCommandMessageID(value string) error {
	if !providerReviewerAssignmentReconciliationCommandIDPattern.MatchString(value) {
		return fmt.Errorf("command_message_id is invalid")
	}
	return nil
}

func ProviderReviewerAssignmentReconciliationResultEvent(value v1.ProviderReviewerAssignmentReconciliationResultEvent) error {
	if value.Kind != v1.EventProviderReviewerAssignmentReconciliationResult {
		return fmt.Errorf("result kind is invalid")
	}
	if err := providerReviewerAssignmentReconciliationAuthority(value.ProviderReviewerAssignmentReconciliationOperationAuthority); err != nil {
		return err
	}
	if err := ProviderReviewerAssignmentReconciliationCommandMessageID(value.CommandMessageID); err != nil {
		return err
	}
	if value.ObservedAt.IsZero() || value.ObservedAt.After(value.LeaseExpiresAt) {
		return fmt.Errorf("observed_at is outside its authority window")
	}
	return providerReviewerAssignmentReconciliationOutcome(value.Outcome, value.ProviderReviewerAssignmentReconciliationOperationAuthority)
}

func providerReviewerAssignmentReconciliationOutcome(outcome v1.ProviderReviewerAssignmentReconciliationResultOutcome, authority v1.ProviderReviewerAssignmentReconciliationOperationAuthority) error {
	switch outcome.Kind {
	case v1.ProviderReviewerAssignmentReconciliationSucceeded:
		if outcome.Code != "" || outcome.RetryAfterMS != nil || len(outcome.IntendedReviewers) == 0 || len(outcome.IntendedReviewers) > 100 {
			return fmt.Errorf("successful outcome is invalid")
		}
		previous, absent := "", 0
		identities := make(map[string]struct{}, len(outcome.IntendedReviewers))
		for _, reviewer := range outcome.IntendedReviewers {
			if err := ID("reviewer_identity_id", reviewer.ReviewerIdentityID); err != nil {
				return err
			}
			if err := reviewerProviderAccountID(authority.Provider, reviewer.ProviderAccountID); err != nil {
				return err
			}
			if previous != "" && reviewer.ProviderAccountID <= previous {
				return fmt.Errorf("observed reviewers must be sorted and unique")
			}
			if _, exists := identities[reviewer.ReviewerIdentityID]; exists {
				return fmt.Errorf("reviewer identity is duplicated")
			}
			if reviewer.Presence != v1.ProviderReviewerAssignmentReconciliationRequested && reviewer.Presence != v1.ProviderReviewerAssignmentReconciliationReviewed && reviewer.Presence != v1.ProviderReviewerAssignmentReconciliationNotPresent {
				return fmt.Errorf("reviewer presence is invalid")
			}
			if reviewer.Presence == v1.ProviderReviewerAssignmentReconciliationNotPresent {
				absent++
			}
			previous = reviewer.ProviderAccountID
			identities[reviewer.ReviewerIdentityID] = struct{}{}
		}
		if v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(observedAsIntended(outcome.IntendedReviewers)) != authority.IntendedReviewerSetDigest {
			return fmt.Errorf("observed reviewers do not bind intended set")
		}
		if outcome.RequestState != v1.ProviderReviewerAssignmentReconciliationRequestOpen && outcome.RequestState != v1.ProviderReviewerAssignmentReconciliationRequestClosed && outcome.RequestState != v1.ProviderReviewerAssignmentReconciliationRequestMerged {
			return fmt.Errorf("request_state is invalid")
		}
		if err := GitCommitSHA("observed_head_sha", outcome.ObservedHeadSHA); err != nil {
			return err
		}
		drifted := outcome.RequestState != v1.ProviderReviewerAssignmentReconciliationRequestOpen || outcome.ObservedHeadSHA != authority.ExpectedHeadSHA
		if drifted != (outcome.Conclusion == v1.ProviderReviewerAssignmentReconciliationSuperseded) {
			return fmt.Errorf("superseded conclusion is inconsistent")
		}
		if drifted {
			return nil
		}
		expected := v1.ProviderReviewerAssignmentReconciliationPartial
		if absent == 0 {
			expected = v1.ProviderReviewerAssignmentReconciliationSatisfied
		} else if absent == len(outcome.IntendedReviewers) {
			expected = v1.ProviderReviewerAssignmentReconciliationAbsent
		}
		if outcome.Conclusion != expected {
			return fmt.Errorf("conclusion does not match presence")
		}
	case v1.ProviderReviewerAssignmentReconciliationRetryable:
		if outcome.Conclusion != "" || outcome.RequestState != "" || outcome.ObservedHeadSHA != "" || len(outcome.IntendedReviewers) != 0 || (outcome.Code != v1.ProviderReviewerAssignmentReconciliationProviderUnavailable && outcome.Code != v1.ProviderReviewerAssignmentReconciliationRateLimited) || outcome.RetryAfterMS != nil && *outcome.RetryAfterMS > maxSourceControlRetryAfterMS {
			return fmt.Errorf("retryable outcome is invalid")
		}
	case v1.ProviderReviewerAssignmentReconciliationPermanent:
		if outcome.Conclusion != "" || outcome.RequestState != "" || outcome.ObservedHeadSHA != "" || len(outcome.IntendedReviewers) != 0 || outcome.RetryAfterMS != nil || !providerReviewerAssignmentReconciliationPermanent(outcome.Code) {
			return fmt.Errorf("permanent outcome is invalid")
		}
	default:
		return fmt.Errorf("outcome kind is unsupported")
	}
	return nil
}

func observedAsIntended(values []v1.ProviderReviewerAssignmentReconciliationObservedReviewer) []v1.ProviderReviewerAssignmentReconciliationIntendedReviewer {
	result := make([]v1.ProviderReviewerAssignmentReconciliationIntendedReviewer, len(values))
	for i, value := range values {
		result[i] = v1.ProviderReviewerAssignmentReconciliationIntendedReviewer{ReviewerIdentityID: value.ReviewerIdentityID, ProviderAccountID: value.ProviderAccountID}
	}
	return result
}

func providerReviewerAssignmentReconciliationPermanent(code v1.ProviderReviewerAssignmentReconciliationErrorCode) bool {
	switch code {
	case v1.ProviderReviewerAssignmentReconciliationRequestNotFound, v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch, v1.ProviderReviewerAssignmentReconciliationInstallationInactive, v1.ProviderReviewerAssignmentReconciliationInstallationAuthorityChanged, v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied, v1.ProviderReviewerAssignmentReconciliationUnsupportedProviderInstance, v1.ProviderReviewerAssignmentReconciliationObservationIncomplete, v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse:
		return true
	}
	return false
}
