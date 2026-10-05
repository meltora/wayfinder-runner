package sourcepublication

import (
	"strconv"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type providerReviewCompletionEvidence struct {
	mergeCommitSHA                 string
	targetBranch                   string
	targetHeadSHA                  string
	mergeCommitReachableFromTarget bool
	mergedAt                       time.Time
	providerUpdatedAt              time.Time
	mergedByProviderAccountID      string
}

func providerReviewCompletionRequested(op v1.ProviderReviewObservationOperation) bool {
	return op.SchemaVersion == v1.ProviderReviewObservationCompletionVersion &&
		op.Purpose == v1.ProviderReviewObservationCompletionRevalidation
}

func validProviderReviewCommitSHA(value string) bool {
	return validation.GitCommitSHA("provider_commit_sha", value) == nil
}

func validProviderReviewTargetBranch(value string) bool {
	return validation.GitRef("provider_target_branch", value) == nil
}

func validProviderReviewCompletionEvidence(e providerReviewCompletionEvidence, now time.Time) bool {
	accountID, err := strconv.ParseUint(e.mergedByProviderAccountID, 10, 64)
	return validProviderReviewCommitSHA(e.mergeCommitSHA) &&
		validProviderReviewTargetBranch(e.targetBranch) &&
		validProviderReviewCommitSHA(e.targetHeadSHA) &&
		!e.mergedAt.IsZero() && !e.providerUpdatedAt.IsZero() &&
		!e.mergedAt.After(e.providerUpdatedAt) && !e.providerUpdatedAt.After(now) &&
		err == nil && accountID != 0 && strconv.FormatUint(accountID, 10) == e.mergedByProviderAccountID
}

func reviewObservationCompletionSuccess(
	op v1.ProviderReviewObservationOperation,
	head string,
	reviewPages int,
	commentPages int,
	reviews []v1.ProviderReviewObservationReviewEvent,
	comments []v1.ProviderReviewObservationComment,
	evidence providerReviewCompletionEvidence,
) v1.ProviderReviewObservationResultOutcome {
	outcome := reviewObservationSuccess(
		op,
		v1.ProviderReviewObservationRequestMerged,
		head,
		reviewPages,
		commentPages,
		reviews,
		comments,
	)
	outcome.MergeCommitSHA = evidence.mergeCommitSHA
	outcome.TargetBranch = evidence.targetBranch
	outcome.TargetHeadSHA = evidence.targetHeadSHA
	outcome.MergeCommitReachableFromTarget = evidence.mergeCommitReachableFromTarget
	outcome.MergedAt = evidence.mergedAt
	outcome.ProviderUpdatedAt = evidence.providerUpdatedAt
	outcome.MergedByProviderAccountID = evidence.mergedByProviderAccountID
	return outcome
}
