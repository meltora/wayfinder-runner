package sourcepublication

import (
	"context"
	"errors"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func assignmentPermanent(
	code v1.ProviderReviewerAssignmentErrorCode,
) v1.ProviderReviewerAssignmentResultOutcome {
	return v1.ProviderReviewerAssignmentResultOutcome{
		Kind: v1.ProviderReviewerAssignmentPermanent,
		Code: code,
	}
}

func assignmentRetryable(
	code v1.ProviderReviewerAssignmentErrorCode,
	retryAfterMS *uint64,
) v1.ProviderReviewerAssignmentResultOutcome {
	return v1.ProviderReviewerAssignmentResultOutcome{
		Kind:         v1.ProviderReviewerAssignmentRetryable,
		Code:         code,
		RetryAfterMS: retryAfterMS,
	}
}

func assignmentRequestFailure(err error) v1.ProviderReviewerAssignmentResultOutcome {
	if errors.Is(err, errInvalidProviderResponse) {
		return assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
	}
	return assignmentRetryable(v1.ProviderReviewerAssignmentProviderUnavailable, nil)
}

func assignmentUncertain() v1.ProviderReviewerAssignmentResultOutcome {
	return v1.ProviderReviewerAssignmentResultOutcome{
		Kind: v1.ProviderReviewerAssignmentUncertain,
		Code: v1.ProviderReviewerAssignmentDeliveryUncertain,
	}
}

func assignmentSucceeded(
	operation v1.ProviderReviewerAssignmentOperation,
	accountIDs []string,
) v1.ProviderReviewerAssignmentResultOutcome {
	recipientsByAccountID := make(map[string]v1.ProviderReviewerAssignmentRecipient, len(operation.Recipients))
	for _, recipient := range operation.Recipients {
		recipientsByAccountID[recipient.ProviderAccountID] = recipient
	}
	requested := make([]v1.ProviderReviewerAssignmentRequestedReviewer, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		recipient := recipientsByAccountID[accountID]
		requested = append(requested, v1.ProviderReviewerAssignmentRequestedReviewer{
			ReviewerIdentityID:  recipient.ReviewerIdentityID,
			ProviderAccountID:   accountID,
			AssignmentPrincipal: recipient.AssignmentPrincipal,
		})
	}
	return v1.ProviderReviewerAssignmentResultOutcome{
		Kind:               v1.ProviderReviewerAssignmentSucceeded,
		RequestedReviewers: requested,
	}
}

func assignmentIdentityVerification(
	operation v1.ProviderReviewerAssignmentOperation,
	recipient v1.ProviderReviewerAssignmentRecipient,
) v1.ReviewerIdentityVerificationOperation {
	return v1.ReviewerIdentityVerificationOperation{
		Provider:            operation.Provider,
		ProviderAccountID:   recipient.ProviderAccountID,
		InstallationID:      operation.InstallationID,
		ProviderInstanceKey: operation.ProviderInstanceKey,
		Repositories: []v1.ReviewerIdentityVerificationRepository{{
			RepositoryID:         operation.RepositoryID,
			RepositoryExternalID: operation.ProviderRepositoryID,
			RepositoryPath:       operation.ProviderRepositoryPath,
		}},
	}
}

func assignmentIdentityFailure(
	verification v1.ReviewerIdentityVerificationResultOutcome,
	recipient v1.ProviderReviewerAssignmentRecipient,
) *v1.ProviderReviewerAssignmentResultOutcome {
	switch verification.Kind {
	case v1.ReviewerIdentityVerificationRetryable:
		code := v1.ProviderReviewerAssignmentProviderUnavailable
		if verification.Code == v1.ReviewerIdentityRateLimited {
			code = v1.ProviderReviewerAssignmentRateLimited
		}
		outcome := assignmentRetryable(code, verification.RetryAfterMS)
		return &outcome
	case v1.ReviewerIdentityVerificationPermanent:
		code := v1.ProviderReviewerAssignmentReviewerNotAssignable
		switch verification.Code {
		case v1.ReviewerIdentityInstallationInactive,
			v1.ReviewerIdentityInstallationNotFound:
			code = v1.ProviderReviewerAssignmentInstallationInactive
		case v1.ReviewerIdentityInstallationPermissionDenied:
			code = v1.ProviderReviewerAssignmentInstallationPermissionDenied
		case v1.ReviewerIdentityAccountNotFound,
			v1.ReviewerIdentityAccountAmbiguous,
			v1.ReviewerIdentityProviderMismatch:
			code = v1.ProviderReviewerAssignmentReviewerIdentityChanged
		}
		outcome := assignmentPermanent(code)
		return &outcome
	case v1.ReviewerIdentityVerificationSucceeded:
		if verification.ProviderUsername != recipient.ProviderUsername ||
			verification.AssignmentPrincipal != recipient.AssignmentPrincipal {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentReviewerIdentityChanged)
			return &outcome
		}
		if len(verification.Repositories) != 1 ||
			verification.Repositories[0].EffectiveReviewerAccess != v1.ReviewerIdentityEligible {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentReviewerNotAssignable)
			return &outcome
		}
		return nil
	default:
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
		return &outcome
	}
}

func (registry *Registry) AssignProviderReviewers(
	ctx context.Context,
	operation v1.ProviderReviewerAssignmentOperation,
) v1.ProviderReviewerAssignmentResultOutcome {
	now := time.Now()
	for _, recipient := range operation.Recipients {
		if !recipient.ProviderAccountProofExpiresAt.After(now) ||
			!recipient.IdentityVerificationExpiresAt.After(now) ||
			operation.LeaseExpiresAt.After(recipient.ProviderAccountProofExpiresAt) ||
			operation.LeaseExpiresAt.After(recipient.IdentityVerificationExpiresAt) {
			return assignmentPermanent(v1.ProviderReviewerAssignmentReviewerIdentityExpired)
		}
	}
	installation, ok := registry.installations[operation.InstallationID]
	if !ok {
		return assignmentPermanent(v1.ProviderReviewerAssignmentInstallationInactive)
	}
	if installation.Provider != operation.Provider {
		return assignmentPermanent(v1.ProviderReviewerAssignmentInstallationPermissionDenied)
	}
	if configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL) != operation.ProviderInstanceKey {
		return assignmentPermanent(v1.ProviderReviewerAssignmentUnsupportedProviderInstance)
	}
	token, err := readToken(installation.TokenFile)
	if err != nil {
		return assignmentPermanent(v1.ProviderReviewerAssignmentInstallationInactive)
	}
	switch installation.Provider {
	case "github":
		return newGitHubPublisher(installation.APIBaseURL, token, registry.httpClient).
			assignReviewers(ctx, operation)
	case "gitlab":
		return newGitLabPublisher(installation.APIBaseURL, token, registry.httpClient).
			assignReviewers(ctx, operation)
	case "bitbucket":
		return newBitbucketPublisher(installation.APIBaseURL, token, registry.httpClient).
			assignReviewers(ctx, operation)
	default:
		return assignmentPermanent(v1.ProviderReviewerAssignmentUnsupportedProviderInstance)
	}
}
