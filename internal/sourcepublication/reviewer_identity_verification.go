package sourcepublication

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func reviewerIdentityPermanent(
	code v1.ReviewerIdentityVerificationErrorCode,
) v1.ReviewerIdentityVerificationResultOutcome {
	return v1.ReviewerIdentityVerificationResultOutcome{
		Kind: v1.ReviewerIdentityVerificationPermanent,
		Code: code,
	}
}

func reviewerIdentityRetryable(
	code v1.ReviewerIdentityVerificationErrorCode,
	retryAfterMS *uint64,
) v1.ReviewerIdentityVerificationResultOutcome {
	return v1.ReviewerIdentityVerificationResultOutcome{
		Kind:         v1.ReviewerIdentityVerificationRetryable,
		Code:         code,
		RetryAfterMS: retryAfterMS,
	}
}

func reviewerIdentityRequestError(
	err error,
	permanentCode v1.ReviewerIdentityVerificationErrorCode,
) v1.ReviewerIdentityVerificationResultOutcome {
	if errors.Is(err, errInvalidProviderResponse) {
		return reviewerIdentityPermanent(permanentCode)
	}
	return reviewerIdentityRetryable(v1.ReviewerIdentityProviderUnavailable, nil)
}

func reviewerIdentityHTTPFailure(
	status int,
	header http.Header,
	notFoundCode v1.ReviewerIdentityVerificationErrorCode,
) v1.ReviewerIdentityVerificationResultOutcome {
	if status == http.StatusRequestTimeout || status >= 500 && status <= 599 {
		return reviewerIdentityRetryable(
			v1.ReviewerIdentityProviderUnavailable,
			providerRetryAfter(header, time.Now()),
		)
	}
	switch status {
	case http.StatusUnauthorized:
		return reviewerIdentityPermanent(v1.ReviewerIdentityInstallationInactive)
	case http.StatusForbidden:
		if header.Get("Retry-After") != "" || header.Get("X-RateLimit-Remaining") == "0" {
			return reviewerIdentityRetryable(v1.ReviewerIdentityRateLimited, providerRetryAfter(header, time.Now()))
		}
		return reviewerIdentityPermanent(v1.ReviewerIdentityInstallationPermissionDenied)
	case http.StatusNotFound:
		return reviewerIdentityPermanent(notFoundCode)
	case http.StatusTooManyRequests:
		return reviewerIdentityRetryable(v1.ReviewerIdentityRateLimited, providerRetryAfter(header, time.Now()))
	default:
		return reviewerIdentityPermanent(notFoundCode)
	}
}

func reviewerIdentitySucceeded(
	username string,
	principal string,
	repositories []v1.ReviewerIdentityRepositoryEvidence,
) v1.ReviewerIdentityVerificationResultOutcome {
	return v1.ReviewerIdentityVerificationResultOutcome{
		Kind:                v1.ReviewerIdentityVerificationSucceeded,
		ProviderUsername:    username,
		AssignmentPrincipal: principal,
		Repositories:        repositories,
	}
}

func configuredProviderInstanceKey(provider, rawBaseURL string) string {
	parsed, err := url.Parse(rawBaseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Host)
	switch provider {
	case "github":
		if host == "api.github.com" {
			return "github.com"
		}
	case "bitbucket":
		if host == "api.bitbucket.org" {
			return "bitbucket.org"
		}
	}
	return host
}

func (registry *Registry) VerifyReviewerIdentity(
	ctx context.Context,
	operation v1.ReviewerIdentityVerificationOperation,
) v1.ReviewerIdentityVerificationResultOutcome {
	installation, ok := registry.installations[operation.InstallationID]
	if !ok {
		return reviewerIdentityPermanent(v1.ReviewerIdentityInstallationNotFound)
	}
	if installation.Provider != operation.Provider {
		return reviewerIdentityPermanent(v1.ReviewerIdentityProviderMismatch)
	}
	if configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL) != operation.ProviderInstanceKey {
		return reviewerIdentityPermanent(v1.ReviewerIdentityUnsupportedProviderInstance)
	}
	token, err := readToken(installation.TokenFile)
	if err != nil {
		return reviewerIdentityPermanent(v1.ReviewerIdentityInstallationInactive)
	}
	switch installation.Provider {
	case "github":
		return newGitHubPublisher(installation.APIBaseURL, token, registry.httpClient).
			verifyReviewerIdentity(ctx, operation)
	case "gitlab":
		return newGitLabPublisher(installation.APIBaseURL, token, registry.httpClient).
			verifyReviewerIdentity(ctx, operation)
	case "bitbucket":
		return newBitbucketPublisher(installation.APIBaseURL, token, registry.httpClient).
			verifyReviewerIdentity(ctx, operation)
	default:
		return reviewerIdentityPermanent(v1.ReviewerIdentityProviderMismatch)
	}
}
