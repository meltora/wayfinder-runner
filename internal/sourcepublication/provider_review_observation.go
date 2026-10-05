package sourcepublication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	v1 "github.com/example/app-runner/api/runner/v1"
	"sort"
	"unicode/utf8"
)

func reviewObservationPermanent(code v1.ProviderReviewObservationErrorCode) v1.ProviderReviewObservationResultOutcome {
	return v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationPermanent, Code: code}
}
func reviewObservationRetryable(code v1.ProviderReviewObservationErrorCode, retry *uint64) v1.ProviderReviewObservationResultOutcome {
	return v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationRetryable, Code: code, RetryAfterMS: retry}
}
func reviewObservationRequestFailure(err error) v1.ProviderReviewObservationResultOutcome {
	if errors.Is(err, errInvalidProviderResponse) {
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
	return reviewObservationRetryable(v1.ProviderReviewObservationProviderUnavailable, nil)
}
func reviewObservationContent(body string) (string, uint64, bool) {
	if !utf8.ValidString(body) || len(body) > 1<<20 {
		return "", 0, false
	}
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:]), uint64(len(body)), true
}
func reviewObservationDecode(body []byte, destination any) bool {
	return utf8.Valid(body) && json.Unmarshal(body, destination) == nil
}
func reviewObservationEmptyContent() (string, uint64) { return reviewObservationContentMust("") }
func reviewObservationContentMust(body string) (string, uint64) {
	digest, count, _ := reviewObservationContent(body)
	return digest, count
}
func reviewObservationSuccess(op v1.ProviderReviewObservationOperation, state v1.ProviderReviewObservationRequestState, head string, reviewPages, commentPages int, reviews []v1.ProviderReviewObservationReviewEvent, comments []v1.ProviderReviewObservationComment) v1.ProviderReviewObservationResultOutcome {
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].ProviderReviewEventID < reviews[j].ProviderReviewEventID })
	sort.Slice(comments, func(i, j int) bool {
		left := string(comments[i].ProviderCommentNamespace) + "\x00" + comments[i].ProviderCommentID
		right := string(comments[j].ProviderCommentNamespace) + "\x00" + comments[j].ProviderCommentID
		return left < right
	})
	return v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationSucceeded, RequestState: state, ObservedHeadSHA: head, StaleHead: head != op.ExpectedHeadSHA, SnapshotSemantics: "current_state", SnapshotComplete: true, Collections: &v1.ProviderReviewObservationCollections{ReviewEvents: v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: reviewPages, ItemsObserved: len(reviews)}, Comments: v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: commentPages, ItemsObserved: len(comments)}}, ReviewEvents: reviews, Comments: comments}
}

func (registry *Registry) ObserveProviderReviews(ctx context.Context, op v1.ProviderReviewObservationOperation, credential *v1.ProviderCredential) v1.ProviderReviewObservationResultOutcome {
	installation, ok := registry.installations[op.InstallationID]
	if !ok {
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationInactive)
	}
	if installation.Provider != op.Provider {
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationPermissionDenied)
	}
	if configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL) != op.ProviderInstanceKey {
		return reviewObservationPermanent(v1.ProviderReviewObservationUnsupportedProviderInstance)
	}
	if providerReviewCompletionRequested(op) && op.Provider != "github" {
		return reviewObservationPermanent(v1.ProviderReviewObservationUnsupportedProviderInstance)
	}
	authority, access := resolveReviewAuthority(installation, credential, op.ProviderRepositoryID)
	switch access {
	case reviewAccessCredentialUnavailable:
		return reviewObservationRetryable(v1.ProviderReviewObservationProviderUnavailable, nil)
	case reviewAccessRepositoryDenied:
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationPermissionDenied)
	case reviewAccessTokenFileInvalid:
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationInactive)
	}
	token := authority.token
	switch op.Provider {
	case "github":
		publisher := registry.newGitHubReviewPublisher(installation, authority)
		if failure := publisher.revalidateReviewObservationSubjects(ctx, op); failure != nil {
			return *failure
		}
		return publisher.observeProviderReviews(ctx, op)
	case "gitlab":
		publisher := newGitLabPublisher(installation.APIBaseURL, token, registry.httpClient)
		if failure := publisher.revalidateReviewObservationSubjects(ctx, op); failure != nil {
			return *failure
		}
		return publisher.observeProviderReviews(ctx, op)
	case "bitbucket":
		publisher := newBitbucketPublisher(installation.APIBaseURL, token, registry.httpClient)
		if failure := publisher.revalidateReviewObservationSubjects(ctx, op); failure != nil {
			return *failure
		}
		return publisher.observeProviderReviews(ctx, op)
	default:
		return reviewObservationPermanent(v1.ProviderReviewObservationUnsupportedProviderInstance)
	}
}

func reviewObservationVerification(op v1.ProviderReviewObservationOperation, s v1.ProviderReviewObservationSubject) v1.ReviewerIdentityVerificationOperation {
	return v1.ReviewerIdentityVerificationOperation{Provider: op.Provider, ProviderAccountID: s.ProviderAccountID, InstallationID: op.InstallationID, ProviderInstanceKey: op.ProviderInstanceKey, Repositories: []v1.ReviewerIdentityVerificationRepository{{RepositoryID: op.RepositoryID, RepositoryExternalID: op.ProviderRepositoryID, RepositoryPath: op.ProviderRepositoryPath}}}
}
func reviewObservationIdentityFailure(v v1.ReviewerIdentityVerificationResultOutcome) *v1.ProviderReviewObservationResultOutcome {
	switch v.Kind {
	case v1.ReviewerIdentityVerificationSucceeded:
		if len(v.Repositories) != 1 || v.Repositories[0].EffectiveReviewerAccess != v1.ReviewerIdentityEligible {
			out := reviewObservationPermanent(v1.ProviderReviewObservationSubjectIdentityChanged)
			return &out
		}
		return nil
	case v1.ReviewerIdentityVerificationRetryable:
		code := v1.ProviderReviewObservationProviderUnavailable
		if v.Code == v1.ReviewerIdentityRateLimited {
			code = v1.ProviderReviewObservationRateLimited
		}
		out := reviewObservationRetryable(code, v.RetryAfterMS)
		return &out
	default:
		code := v1.ProviderReviewObservationSubjectIdentityChanged
		if v.Code == v1.ReviewerIdentityInstallationInactive || v.Code == v1.ReviewerIdentityInstallationNotFound {
			code = v1.ProviderReviewObservationInstallationInactive
		} else if v.Code == v1.ReviewerIdentityInstallationPermissionDenied {
			code = v1.ProviderReviewObservationInstallationPermissionDenied
		}
		out := reviewObservationPermanent(code)
		return &out
	}
}
func (p *githubPublisher) revalidateReviewObservationSubjects(ctx context.Context, op v1.ProviderReviewObservationOperation) *v1.ProviderReviewObservationResultOutcome {
	for _, s := range op.Subjects {
		if failure := reviewObservationIdentityFailure(p.verifyReviewerIdentity(ctx, reviewObservationVerification(op, s))); failure != nil {
			return failure
		}
	}
	return nil
}
func (p *gitLabPublisher) revalidateReviewObservationSubjects(ctx context.Context, op v1.ProviderReviewObservationOperation) *v1.ProviderReviewObservationResultOutcome {
	for _, s := range op.Subjects {
		if failure := reviewObservationIdentityFailure(p.verifyReviewerIdentity(ctx, reviewObservationVerification(op, s))); failure != nil {
			return failure
		}
	}
	return nil
}
func (p *bitbucketPublisher) revalidateReviewObservationSubjects(ctx context.Context, op v1.ProviderReviewObservationOperation) *v1.ProviderReviewObservationResultOutcome {
	for _, s := range op.Subjects {
		if failure := reviewObservationIdentityFailure(p.verifyReviewerIdentity(ctx, reviewObservationVerification(op, s))); failure != nil {
			return failure
		}
	}
	return nil
}
