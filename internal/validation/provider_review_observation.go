package validation

import (
	"fmt"
	v1 "github.com/example/app-runner/api/runner/v1"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	providerReviewObservationOperationIDPattern = regexp.MustCompile(`^provider-review-observation-operation-[0-9a-f]{64}$`)
	providerReviewObservationCommandIDPattern   = regexp.MustCompile(`^provider-review-observation-command-[0-9a-f]{64}$`)
	providerReviewObservationIdempotencyPattern = regexp.MustCompile(`^provider-review-observation-[0-9a-f]{64}$`)
	providerReviewObservationDerivedIDPattern   = regexp.MustCompile(`^derived-[0-9a-f]{64}$`)
)

const providerReviewObservationMaxEvidence = 1000

func providerReviewObservationAuthority(v v1.ProviderReviewObservationAuthority) error {
	switch v.SchemaVersion {
	case v1.ProviderReviewObservationAuthorityVersion:
		if v.Purpose != "" || v.ExpectedTargetBranch != "" {
			return fmt.Errorf("provider review observation authority is invalid")
		}
	case v1.ProviderReviewObservationCompletionVersion:
		if v.Purpose != v1.ProviderReviewObservationCompletionRevalidation || GitRef("expected_target_branch", v.ExpectedTargetBranch) != nil {
			return fmt.Errorf("provider review observation authority is invalid")
		}
	default:
		return fmt.Errorf("provider review observation authority is invalid")
	}
	if !providerReviewObservationOperationIDPattern.MatchString(v.ProviderReviewObservationOperationID) {
		return fmt.Errorf("provider review observation authority is invalid")
	}
	for field, id := range map[string]string{"organization_id": v.OrganizationID, "project_id": v.ProjectID, "workflow_run_id": v.WorkflowRunID, "assignment_delivery_id": v.AssignmentDeliveryID, "assignment_plan_id": v.AssignmentPlanID, "assignment_observation_id": v.AssignmentObservationID, "source_publication_observation_id": v.SourcePublicationObservationID, "repository_id": v.RepositoryID, "installation_id": v.InstallationID, "lease_id": v.LeaseID} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	for field, digest := range map[string]string{"assignment_plan_digest": v.AssignmentPlanDigest, "assignment_result_payload_checksum": v.AssignmentResultPayloadChecksum, "installation_authority_digest": v.InstallationAuthorityDigest, "subject_identity_set_digest": v.SubjectIdentitySetDigest, "operation_digest": v.OperationDigest, "command_digest": v.CommandDigest} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if v.AssignmentDeliveryStateRevision == 0 || v.AssignmentDeliveryStateRevision > maxJSONSafeInteger || v.InstallationStateRevision > maxJSONSafeInteger || v.InstallationAuthorityRevision > maxJSONSafeInteger || v.FencingToken == 0 || v.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("numeric authority is invalid")
	}
	if v.Provider != "github" && v.Provider != "gitlab" && v.Provider != "bitbucket" {
		return fmt.Errorf("provider is unsupported")
	}
	if len(v.ProviderInstanceKey) > 255 || !providerInstanceKeyPattern.MatchString(v.ProviderInstanceKey) || v.Provider == "bitbucket" && v.ProviderInstanceKey != "bitbucket.org" {
		return fmt.Errorf("provider instance is invalid")
	}
	if err := providerReviewerAssignmentRepository(v1.ProviderReviewerAssignmentOperationAuthority{Provider: v.Provider, ProviderInstanceKey: v.ProviderInstanceKey, ProviderRepositoryID: v.ProviderRepositoryID, ProviderRepositoryPath: v.ProviderRepositoryPath}); err != nil {
		return err
	}
	requestID, err := strconv.ParseUint(v.ExternalRequestID, 10, 64)
	if err != nil || requestID == 0 || strconv.FormatUint(requestID, 10) != v.ExternalRequestID {
		return fmt.Errorf("external request ID is invalid")
	}
	if err := GitCommitSHA("expected_head_sha", v.ExpectedHeadSHA); err != nil {
		return err
	}
	if !providerReviewObservationIdempotencyPattern.MatchString(v.IdempotencyKey) {
		return fmt.Errorf("idempotency key is invalid")
	}
	if v.MaxCollectionPages != 10 || v.CollectionPageSize != 100 {
		return fmt.Errorf("collection bounds are invalid")
	}
	if v.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("lease expiry is required")
	}
	return nil
}

func ProviderReviewObservationOperation(v v1.ProviderReviewObservationOperation) error {
	if err := providerReviewObservationAuthority(v.ProviderReviewObservationAuthority); err != nil {
		return err
	}
	if len(v.Subjects) == 0 || len(v.Subjects) > 100 {
		return fmt.Errorf("subjects count is invalid")
	}
	sets := []map[string]struct{}{make(map[string]struct{}), make(map[string]struct{}), make(map[string]struct{}), make(map[string]struct{}), make(map[string]struct{})}
	previous := ""
	for _, s := range v.Subjects {
		ids := []string{s.ReviewerIdentityID, s.UserID, s.OrganizationMembershipID, s.ProviderAccountProofID, s.IdentityVerificationID}
		for i, id := range ids {
			if err := ID("subject_id", id); err != nil {
				return err
			}
			if _, dup := sets[i][id]; dup {
				return fmt.Errorf("subject identity is duplicated")
			}
			sets[i][id] = struct{}{}
		}
		if previous != "" && s.ProviderAccountID <= previous {
			return fmt.Errorf("subjects must be sorted and unique")
		}
		previous = s.ProviderAccountID
		if err := reviewerProviderAccountID(v.Provider, s.ProviderAccountID); err != nil {
			return err
		}
		if s.IdentityRevision == 0 || s.ProviderAccountProofRevision == 0 || s.IdentityStateRevision > maxJSONSafeInteger || s.IdentityRevision > maxJSONSafeInteger || s.ProviderAccountProofStateRevision > maxJSONSafeInteger || s.ProviderAccountProofRevision > maxJSONSafeInteger {
			return fmt.Errorf("subject revision is invalid")
		}
		if err := SHA256Digest("provider_account_proof_digest", s.ProviderAccountProofDigest); err != nil {
			return err
		}
		if err := SHA256Digest("identity_verification_digest", s.IdentityVerificationDigest); err != nil {
			return err
		}
		if s.ProviderAccountProofExpiresAt.IsZero() || s.IdentityVerificationExpiresAt.IsZero() || s.IdentityVerificationExpiresAt.After(s.ProviderAccountProofExpiresAt) || v.LeaseExpiresAt.After(s.ProviderAccountProofExpiresAt) || v.LeaseExpiresAt.After(s.IdentityVerificationExpiresAt) {
			return fmt.Errorf("subject authority expiry is invalid")
		}
	}
	if v1.ProviderReviewObservationSubjectIdentitySetDigest(v.Subjects) != v.SubjectIdentitySetDigest {
		return fmt.Errorf("subject identity digest mismatch")
	}
	return nil
}

func ProviderReviewObservationCommandMessageID(v string) error {
	if !providerReviewObservationCommandIDPattern.MatchString(v) {
		return fmt.Errorf("command message ID is invalid")
	}
	return nil
}
func metadataID(v string) bool {
	if len(v) == 0 || len(v) > 512 || strings.TrimSpace(v) != v || strings.Contains(v, "://") {
		return false
	}
	for _, r := range v {
		if r <= 0x1f || r == 0x7f {
			return false
		}
	}
	return true
}
func reviewState(v v1.ProviderReviewObservationReviewState) bool {
	switch v {
	case v1.ProviderReviewObservationUnreviewed, v1.ProviderReviewObservationReviewStarted, v1.ProviderReviewObservationReviewed, v1.ProviderReviewObservationCommented, v1.ProviderReviewObservationApproved, v1.ProviderReviewObservationChangesRequested, v1.ProviderReviewObservationDismissed, v1.ProviderReviewObservationUnapproved:
		return true
	}
	return false
}
func reviewKind(v v1.ProviderReviewObservationReviewEventKind) bool {
	switch v {
	case v1.ProviderReviewObservationReview, v1.ProviderReviewObservationReviewerState, v1.ProviderReviewObservationApproval, v1.ProviderReviewObservationChangeRequest, v1.ProviderReviewObservationDismissal:
		return true
	}
	return false
}
func canonicalTime(v time.Time) bool { return !v.IsZero() }
func ProviderReviewObservationResultEvent(e v1.ProviderReviewObservationResultEvent) error {
	if e.Kind != v1.EventProviderReviewObservationResult {
		return fmt.Errorf("result kind is invalid")
	}
	if err := providerReviewObservationAuthority(e.ProviderReviewObservationAuthority); err != nil {
		return err
	}
	if err := ProviderReviewObservationCommandMessageID(e.CommandMessageID); err != nil {
		return err
	}
	if e.ObservedAt.IsZero() || e.ObservedAt.After(e.LeaseExpiresAt) {
		return fmt.Errorf("observation time is invalid")
	}
	o := e.Outcome
	switch o.Kind {
	case v1.ProviderReviewObservationSucceeded:
		if o.Code != "" || o.RetryAfterMS != nil || o.RequestState != v1.ProviderReviewObservationRequestOpen && o.RequestState != v1.ProviderReviewObservationRequestClosed && o.RequestState != v1.ProviderReviewObservationRequestMerged || o.ObservedHeadSHA == "" || o.SnapshotSemantics != "current_state" || !o.SnapshotComplete || o.Collections == nil {
			return fmt.Errorf("success outcome is invalid")
		}
		if err := GitCommitSHA("observed_head_sha", o.ObservedHeadSHA); err != nil {
			return err
		}
		if o.StaleHead != (o.ObservedHeadSHA != e.ExpectedHeadSHA) {
			return fmt.Errorf("stale head is invalid")
		}
		if len(o.ReviewEvents) > providerReviewObservationMaxEvidence || len(o.Comments) > providerReviewObservationMaxEvidence {
			return fmt.Errorf("evidence bound exceeded")
		}
		if err := collection(o.Collections.ReviewEvents, len(o.ReviewEvents)); err != nil {
			return err
		}
		if err := collection(o.Collections.Comments, len(o.Comments)); err != nil {
			return err
		}
		previous := ""
		for _, r := range o.ReviewEvents {
			if previous != "" && r.ProviderReviewEventID <= previous {
				return fmt.Errorf("review events are not sorted unique")
			}
			previous = r.ProviderReviewEventID
			if err := reviewEvent(e.ProviderReviewObservationAuthority, r, e.ObservedAt); err != nil {
				return err
			}
		}
		previous = ""
		for _, c := range o.Comments {
			key := string(c.ProviderCommentNamespace) + "\x00" + c.ProviderCommentID
			if previous != "" && key <= previous {
				return fmt.Errorf("comments are not sorted unique")
			}
			previous = key
			if err := comment(e.Provider, c, e.ObservedAt); err != nil {
				return err
			}
		}
		if e.SchemaVersion == v1.ProviderReviewObservationAuthorityVersion {
			if providerReviewObservationHasCompletionEvidence(o) {
				return fmt.Errorf("v1 success contains completion evidence")
			}
		} else if err := providerReviewObservationCompletionEvidence(e.ProviderReviewObservationAuthority, o, e.ObservedAt); err != nil {
			return err
		}
	case v1.ProviderReviewObservationRetryable:
		if o.RequestState != "" || o.ObservedHeadSHA != "" || o.SnapshotSemantics != "" || o.SnapshotComplete || o.Collections != nil || len(o.ReviewEvents) != 0 || len(o.Comments) != 0 || providerReviewObservationHasCompletionEvidence(o) || (o.Code != v1.ProviderReviewObservationProviderUnavailable && o.Code != v1.ProviderReviewObservationRateLimited) || o.RetryAfterMS != nil && *o.RetryAfterMS > maxSourceControlRetryAfterMS {
			return fmt.Errorf("retryable outcome is invalid")
		}
	case v1.ProviderReviewObservationPermanent:
		if o.RequestState != "" || o.ObservedHeadSHA != "" || o.SnapshotSemantics != "" || o.SnapshotComplete || o.Collections != nil || len(o.ReviewEvents) != 0 || len(o.Comments) != 0 || providerReviewObservationHasCompletionEvidence(o) || o.RetryAfterMS != nil || !reviewObservationPermanent(o.Code) {
			return fmt.Errorf("permanent outcome is invalid")
		}
	default:
		return fmt.Errorf("outcome kind is invalid")
	}
	return nil
}

func providerReviewObservationHasCompletionEvidence(o v1.ProviderReviewObservationResultOutcome) bool {
	return o.MergeCommitSHA != "" || o.TargetBranch != "" || o.TargetHeadSHA != "" ||
		o.MergeCommitReachableFromTarget || !o.MergedAt.IsZero() || !o.ProviderUpdatedAt.IsZero() ||
		o.MergedByProviderAccountID != ""
}

func providerReviewObservationCompletionEvidence(authority v1.ProviderReviewObservationAuthority, o v1.ProviderReviewObservationResultOutcome, observedAt time.Time) error {
	if authority.Provider != "github" {
		return fmt.Errorf("provider cannot supply exact completion evidence")
	}
	if o.RequestState != v1.ProviderReviewObservationRequestMerged {
		return fmt.Errorf("completion revalidation request is not merged")
	}
	if err := GitCommitSHA("merge_commit_sha", o.MergeCommitSHA); err != nil {
		return err
	}
	if err := GitRef("target_branch", o.TargetBranch); err != nil {
		return err
	}
	if o.TargetBranch != authority.ExpectedTargetBranch {
		return fmt.Errorf("observed target branch does not match completion authority")
	}
	if err := GitCommitSHA("target_head_sha", o.TargetHeadSHA); err != nil {
		return err
	}
	if o.MergedAt.IsZero() || o.ProviderUpdatedAt.IsZero() || o.MergedAt.After(o.ProviderUpdatedAt) || o.ProviderUpdatedAt.After(observedAt) {
		return fmt.Errorf("provider merge timestamps are invalid")
	}
	if err := reviewerProviderAccountID(authority.Provider, o.MergedByProviderAccountID); err != nil {
		return fmt.Errorf("merged-by provider account ID is invalid")
	}
	return nil
}
func collection(v v1.ProviderReviewObservationCollectionCompleteness, count int) error {
	if !v.Complete || v.PagesRead < 1 || v.PagesRead > 10 || v.ItemsObserved != count || v.ItemsObserved < 0 || v.ItemsObserved > 1000 || v.ItemsObserved > v.PagesRead*100 {
		return fmt.Errorf("collection completeness is invalid")
	}
	return nil
}
func reviewEvent(a v1.ProviderReviewObservationAuthority, r v1.ProviderReviewObservationReviewEvent, observed time.Time) error {
	if !metadataID(r.ProviderReviewEventID) || r.ProviderReviewEventRevision == 0 || r.ProviderReviewEventRevision > maxJSONSafeInteger || reviewerProviderAccountID(a.Provider, r.ProviderAccountID) != nil || !reviewKind(r.ReviewEventKind) || !reviewState(r.ReviewState) || !canonicalTime(r.OccurredAt) || r.OccurredAt.After(observed) || r.ProviderUpdatedAt != nil && (r.ProviderUpdatedAt.Before(r.OccurredAt) || r.ProviderUpdatedAt.After(observed)) || r.CommitSHA != "" && GitCommitSHA("commit_sha", r.CommitSHA) != nil || SHA256Digest("content_digest", r.ContentDigest) != nil || r.ContentByteCount > 1<<20 {
		return fmt.Errorf("review event is invalid")
	}
	if numeric, err := strconv.ParseUint(r.ProviderReviewEventID, 10, 64); err == nil {
		if numeric == 0 || strconv.FormatUint(numeric, 10) != r.ProviderReviewEventID {
			return fmt.Errorf("native review ID is invalid")
		}
	} else {
		expected := v1.ProviderReviewObservationDerivedReviewEventID(v1.ProviderReviewObservationDerivedReviewEventIdentity{Provider: a.Provider, ProviderInstanceKey: a.ProviderInstanceKey, ProviderRepositoryID: a.ProviderRepositoryID, ExternalRequestID: a.ExternalRequestID, ReviewEventKind: r.ReviewEventKind, ProviderAccountID: r.ProviderAccountID, OccurredAt: r.OccurredAt})
		if !providerReviewObservationDerivedIDPattern.MatchString(r.ProviderReviewEventID) || r.ProviderReviewEventID != expected {
			return fmt.Errorf("derived review ID is invalid")
		}
	}
	return nil
}
func comment(provider string, c v1.ProviderReviewObservationComment, observed time.Time) error {
	id, err := strconv.ParseUint(c.ProviderCommentID, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != c.ProviderCommentID || c.ProviderCommentRevision == 0 || c.ProviderCommentRevision > maxJSONSafeInteger || reviewerProviderAccountID(provider, c.ProviderAccountID) != nil || !canonicalTime(c.CreatedAt) || !canonicalTime(c.UpdatedAt) || c.UpdatedAt.Before(c.CreatedAt) || c.UpdatedAt.After(observed) || c.CreatedAt.After(observed) || SHA256Digest("content_digest", c.ContentDigest) != nil || c.ContentByteCount > 1<<20 || provider != "bitbucket" && c.Deleted {
		return fmt.Errorf("comment is invalid")
	}
	switch c.ProviderCommentNamespace {
	case v1.ProviderReviewObservationDiscussion, v1.ProviderReviewObservationIssue, v1.ProviderReviewObservationPullRequest, v1.ProviderReviewObservationReviewNamespace:
	default:
		return fmt.Errorf("comment namespace is invalid")
	}
	switch c.CommentKind {
	case v1.ProviderReviewObservationGeneral:
	case v1.ProviderReviewObservationInline:
		if c.Position == nil {
			return fmt.Errorf("inline position required")
		}
	case v1.ProviderReviewObservationThreadReply:
		if c.ParentCommentID == "" {
			return fmt.Errorf("parent required")
		}
	default:
		return fmt.Errorf("comment kind invalid")
	}
	if c.ParentCommentID != "" {
		if parent, err := strconv.ParseUint(c.ParentCommentID, 10, 64); err != nil || parent == 0 || strconv.FormatUint(parent, 10) != c.ParentCommentID {
			return fmt.Errorf("parent invalid")
		}
	}
	if c.Position != nil {
		if c.Position.FilePath == "" || strings.HasPrefix(c.Position.FilePath, "/") || strings.Contains(c.Position.FilePath, "..") || c.Position.Line == 0 || (c.Position.Side != "left" && c.Position.Side != "right") || GitCommitSHA("position.commit_sha", c.Position.CommitSHA) != nil {
			return fmt.Errorf("position invalid")
		}
	}
	return nil
}
func reviewObservationPermanent(c v1.ProviderReviewObservationErrorCode) bool {
	switch c {
	case v1.ProviderReviewObservationRequestNotFound, v1.ProviderReviewObservationRepositoryMismatch, v1.ProviderReviewObservationInstallationInactive, v1.ProviderReviewObservationInstallationAuthorityChanged, v1.ProviderReviewObservationInstallationPermissionDenied, v1.ProviderReviewObservationSubjectIdentityChanged, v1.ProviderReviewObservationSubjectIdentityExpired, v1.ProviderReviewObservationProviderAccountProofChanged, v1.ProviderReviewObservationProviderAccountProofExpired, v1.ProviderReviewObservationUnsupportedProviderInstance, v1.ProviderReviewObservationIncomplete, v1.ProviderReviewObservationInvalidProviderResponse:
		return true
	}
	return false
}
