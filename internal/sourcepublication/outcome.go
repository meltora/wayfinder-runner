package sourcepublication

import (
	"errors"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var errInvalidProviderResponse = errors.New("invalid provider response")

func permanent(code v1.SourceControlErrorCode) v1.SourcePublicationResultOutcome {
	return v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationPermanent, Code: code}
}

func retryable(code v1.SourceControlErrorCode, retryAfterMS *uint64) v1.SourcePublicationResultOutcome {
	return v1.SourcePublicationResultOutcome{
		Kind: v1.SourcePublicationRetryable, Code: code, RetryAfterMS: retryAfterMS,
	}
}

func publicationRequestFailure(err error) v1.SourcePublicationResultOutcome {
	if errors.Is(err, errInvalidProviderResponse) {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	return retryable(v1.SourceControlProviderUnavailable, nil)
}
