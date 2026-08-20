package sourcepublication

import v1 "github.com/example/app-runner/api/runner/v1"

func permanent(code v1.SourceControlErrorCode) v1.SourcePublicationResultOutcome {
	return v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationPermanent, Code: code}
}

func retryable(code v1.SourceControlErrorCode, retryAfterMS *uint64) v1.SourcePublicationResultOutcome {
	return v1.SourcePublicationResultOutcome{
		Kind: v1.SourcePublicationRetryable, Code: code, RetryAfterMS: retryAfterMS,
	}
}
