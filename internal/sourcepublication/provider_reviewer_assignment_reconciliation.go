package sourcepublication

import (
	"context"
	"errors"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func reconciliationPermanent(code v1.ProviderReviewerAssignmentReconciliationErrorCode) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	return v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationPermanent, Code: code}
}

func reconciliationRetryable(code v1.ProviderReviewerAssignmentReconciliationErrorCode, retryAfterMS *uint64) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	return v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationRetryable, Code: code, RetryAfterMS: retryAfterMS}
}

func reconciliationRequestFailure(err error) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	if errors.Is(err, errInvalidProviderResponse) {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	return reconciliationRetryable(v1.ProviderReviewerAssignmentReconciliationProviderUnavailable, nil)
}

func reconciliationSucceeded(operation v1.ProviderReviewerAssignmentReconciliationOperation, state v1.ProviderReviewerAssignmentReconciliationRequestState, head string, presence map[string]v1.ProviderReviewerAssignmentReconciliationPresence) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	observed := make([]v1.ProviderReviewerAssignmentReconciliationObservedReviewer, len(operation.IntendedReviewers))
	absent := 0
	for index, intended := range operation.IntendedReviewers {
		value := presence[intended.ProviderAccountID]
		if value == "" {
			value = v1.ProviderReviewerAssignmentReconciliationNotPresent
		}
		if value == v1.ProviderReviewerAssignmentReconciliationNotPresent {
			absent++
		}
		observed[index] = v1.ProviderReviewerAssignmentReconciliationObservedReviewer{ReviewerIdentityID: intended.ReviewerIdentityID, ProviderAccountID: intended.ProviderAccountID, Presence: value}
	}
	conclusion := v1.ProviderReviewerAssignmentReconciliationPartial
	if state != v1.ProviderReviewerAssignmentReconciliationRequestOpen || head != operation.ExpectedHeadSHA {
		conclusion = v1.ProviderReviewerAssignmentReconciliationSuperseded
	} else if absent == 0 {
		conclusion = v1.ProviderReviewerAssignmentReconciliationSatisfied
	} else if absent == len(observed) {
		conclusion = v1.ProviderReviewerAssignmentReconciliationAbsent
	}
	return v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationSucceeded, Conclusion: conclusion, RequestState: state, ObservedHeadSHA: head, IntendedReviewers: observed}
}

func (registry *Registry) ReconcileProviderReviewerAssignment(ctx context.Context, operation v1.ProviderReviewerAssignmentReconciliationOperation, credential *v1.ProviderCredential) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	installation, ok := registry.installations[operation.InstallationID]
	if !ok {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationInactive)
	}
	if installation.Provider != operation.Provider {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied)
	}
	if configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL) != operation.ProviderInstanceKey {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationUnsupportedProviderInstance)
	}
	authority, access := resolveReviewAuthority(installation, credential, operation.ProviderRepositoryID)
	switch access {
	case reviewAccessCredentialUnavailable:
		return reconciliationRetryable(v1.ProviderReviewerAssignmentReconciliationProviderUnavailable, nil)
	case reviewAccessRepositoryDenied:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied)
	case reviewAccessTokenFileInvalid:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationInactive)
	}
	token := authority.token
	switch installation.Provider {
	case "github":
		return registry.newGitHubReviewPublisher(installation, authority).reconcileReviewerAssignment(ctx, operation)
	case "gitlab":
		return newGitLabPublisher(installation.APIBaseURL, token, registry.httpClient).reconcileReviewerAssignment(ctx, operation)
	case "bitbucket":
		return newBitbucketPublisher(installation.APIBaseURL, token, registry.httpClient).reconcileReviewerAssignment(ctx, operation)
	default:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationUnsupportedProviderInstance)
	}
}

func upgradePresence(values map[string]v1.ProviderReviewerAssignmentReconciliationPresence, id string, presence v1.ProviderReviewerAssignmentReconciliationPresence) {
	if current := values[id]; current == v1.ProviderReviewerAssignmentReconciliationReviewed || current == presence {
		return
	}
	values[id] = presence
}
