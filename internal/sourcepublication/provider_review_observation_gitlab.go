package sourcepublication

import (
	"context"
	v1 "github.com/example/app-runner/api/runner/v1"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (p *gitLabPublisher) observeProviderReviews(ctx context.Context, op v1.ProviderReviewObservationOperation) v1.ProviderReviewObservationResultOutcome {
	if providerReviewCompletionRequested(op) {
		// GitLab's merge_user is explicitly ambiguous: it can identify either the
		// merger or the user who enabled auto-merge. It cannot satisfy the exact
		// merged_by_provider_account_id contract without inference.
		return reviewObservationPermanent(v1.ProviderReviewObservationUnsupportedProviderInstance)
	}
	repoID, err := strconv.ParseUint(op.ProviderRepositoryID, 10, 64)
	if err != nil || repoID == 0 || strconv.FormatUint(repoID, 10) != op.ProviderRepositoryID {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	projectPath := "/projects/" + url.PathEscape(op.ProviderRepositoryID)
	response, err := p.request(ctx, http.MethodGet, projectPath, nil, nil)
	if err != nil {
		return reviewObservationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitLabReviewObservationFailure(response, v1.ProviderReviewObservationRepositoryMismatch)
	}
	var project struct {
		ID                uint64 `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if !reviewObservationDecode(response.body, &project) || project.ID != repoID || project.PathWithNamespace == "" {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	iid, err := canonicalPositiveInt64(op.ExternalRequestID)
	if err != nil {
		return reviewObservationPermanent(v1.ProviderReviewObservationRequestNotFound)
	}
	requestPath := projectPath + "/merge_requests/" + strconv.FormatInt(iid, 10)
	response, err = p.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		return reviewObservationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitLabReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
	}
	var request struct {
		IID   int64  `json:"iid"`
		State string `json:"state"`
		SHA   string `json:"sha"`
	}
	if !reviewObservationDecode(response.body, &request) || request.IID != iid || request.SHA == "" {
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
	state := v1.ProviderReviewObservationRequestState("")
	switch request.State {
	case "opened":
		state = v1.ProviderReviewObservationRequestOpen
	case "closed":
		state = v1.ProviderReviewObservationRequestClosed
	case "merged":
		state = v1.ProviderReviewObservationRequestMerged
	default:
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
	reviews, reviewPages, failure := p.gitLabReviewEvents(ctx, requestPath, op)
	if failure != nil {
		return *failure
	}
	comments, commentPages, failure := p.gitLabReviewComments(ctx, requestPath)
	if failure != nil {
		return *failure
	}
	return reviewObservationSuccess(op, state, request.SHA, reviewPages, commentPages, reviews, comments)
}
func (p *gitLabPublisher) gitLabReviewEvents(ctx context.Context, path string, op v1.ProviderReviewObservationOperation) ([]v1.ProviderReviewObservationReviewEvent, int, *v1.ProviderReviewObservationResultOutcome) {
	result := []v1.ProviderReviewObservationReviewEvent{}
	seen := map[string]struct{}{}
	emptyDigest, emptyCount := reviewObservationEmptyContent()
	for page := 1; page <= 10; page++ {
		response, err := p.request(ctx, http.MethodGet, path+"/reviewers", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			out := reviewObservationRequestFailure(err)
			return nil, 0, &out
		}
		if response.status < 200 || response.status >= 300 {
			out := classifyGitLabReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
			return nil, 0, &out
		}
		var values []struct {
			User struct {
				ID       uint64 `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
			State     string    `json:"state"`
			CreatedAt time.Time `json:"created_at"`
		}
		if !reviewObservationDecode(response.body, &values) || len(values) > 100 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, 0, &out
		}
		for _, item := range values {
			if item.User.ID == 0 || item.CreatedAt.IsZero() {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			state, kind, ok := gitLabReviewState(item.State)
			if !ok {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			accountID := strconv.FormatUint(item.User.ID, 10)
			id := v1.ProviderReviewObservationDerivedReviewEventID(v1.ProviderReviewObservationDerivedReviewEventIdentity{Provider: op.Provider, ProviderInstanceKey: op.ProviderInstanceKey, ProviderRepositoryID: op.ProviderRepositoryID, ExternalRequestID: op.ExternalRequestID, ReviewEventKind: kind, ProviderAccountID: accountID, OccurredAt: item.CreatedAt})
			if _, dup := seen[id]; dup {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			seen[id] = struct{}{}
			revision := item.CreatedAt.UnixMilli()
			if revision <= 0 {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			result = append(result, v1.ProviderReviewObservationReviewEvent{ProviderReviewEventID: id, ProviderReviewEventRevision: uint64(revision), ProviderAccountID: accountID, ReviewEventKind: kind, ReviewState: state, OccurredAt: item.CreatedAt, ContentDigest: emptyDigest, ContentByteCount: emptyCount})
		}
		hasNext, validNext := gitLabObservationHasNext(response.header, page)
		if !validNext {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, 0, &out
		}
		if !hasNext {
			return result, page, nil
		}
		if len(values) == 0 || page == 10 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationIncomplete)
			return nil, 0, &out
		}
	}
	panic("unreachable")
}
func gitLabReviewState(state string) (v1.ProviderReviewObservationReviewState, v1.ProviderReviewObservationReviewEventKind, bool) {
	switch state {
	case "unreviewed":
		return v1.ProviderReviewObservationUnreviewed, v1.ProviderReviewObservationReviewerState, true
	case "review_started":
		return v1.ProviderReviewObservationReviewStarted, v1.ProviderReviewObservationReviewerState, true
	case "reviewed":
		return v1.ProviderReviewObservationReviewed, v1.ProviderReviewObservationReviewerState, true
	case "requested_changes":
		return v1.ProviderReviewObservationChangesRequested, v1.ProviderReviewObservationChangeRequest, true
	case "approved":
		return v1.ProviderReviewObservationApproved, v1.ProviderReviewObservationApproval, true
	case "unapproved":
		return v1.ProviderReviewObservationUnapproved, v1.ProviderReviewObservationApproval, true
	}
	return "", "", false
}
func (p *gitLabPublisher) gitLabReviewComments(ctx context.Context, path string) ([]v1.ProviderReviewObservationComment, int, *v1.ProviderReviewObservationResultOutcome) {
	result := []v1.ProviderReviewObservationComment{}
	seen := map[uint64]struct{}{}
	for page := 1; page <= 10; page++ {
		response, err := p.request(ctx, http.MethodGet, path+"/notes", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			out := reviewObservationRequestFailure(err)
			return nil, 0, &out
		}
		if response.status < 200 || response.status >= 300 {
			out := classifyGitLabReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
			return nil, 0, &out
		}
		var values []struct {
			ID         uint64    `json:"id"`
			Body       *string   `json:"body"`
			CreatedAt  time.Time `json:"created_at"`
			UpdatedAt  time.Time `json:"updated_at"`
			Resolvable bool      `json:"resolvable"`
			Resolved   bool      `json:"resolved"`
			Type       string    `json:"type"`
			Author     struct {
				ID uint64 `json:"id"`
			} `json:"author"`
			Position *struct {
				NewPath string `json:"new_path"`
				OldPath string `json:"old_path"`
				NewLine uint64 `json:"new_line"`
				OldLine uint64 `json:"old_line"`
				HeadSHA string `json:"head_sha"`
			} `json:"position"`
		}
		if !reviewObservationDecode(response.body, &values) || len(values) > 100 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, 0, &out
		}
		for _, item := range values {
			if item.ID == 0 || item.Body == nil || item.Author.ID == 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			if _, dup := seen[item.ID]; dup {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			seen[item.ID] = struct{}{}
			digest, count, ok := reviewObservationContent(*item.Body)
			if !ok {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			kind := v1.ProviderReviewObservationGeneral
			var position *v1.ProviderReviewObservationCommentPosition
			if item.Type == "DiffNote" {
				if item.Position == nil {
					out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
					return nil, 0, &out
				}
				kind = v1.ProviderReviewObservationInline
				path, line, side := item.Position.NewPath, item.Position.NewLine, "right"
				if line == 0 {
					path, line, side = item.Position.OldPath, item.Position.OldLine, "left"
				}
				position = &v1.ProviderReviewObservationCommentPosition{FilePath: path, Line: line, Side: side, CommitSHA: item.Position.HeadSHA}
			} else if item.Type != "" && item.Type != "DiscussionNote" {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			revision := item.UpdatedAt.UnixMilli()
			if revision <= 0 {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			result = append(result, v1.ProviderReviewObservationComment{ProviderCommentID: strconv.FormatUint(item.ID, 10), ProviderCommentRevision: uint64(revision), ProviderAccountID: strconv.FormatUint(item.Author.ID, 10), ProviderCommentNamespace: v1.ProviderReviewObservationDiscussion, CommentKind: kind, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, Position: position, Resolved: item.Resolvable && item.Resolved, Deleted: false, ContentDigest: digest, ContentByteCount: count})
		}
		hasNext, validNext := gitLabObservationHasNext(response.header, page)
		if !validNext {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, 0, &out
		}
		if !hasNext {
			return result, page, nil
		}
		if len(values) == 0 || page == 10 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationIncomplete)
			return nil, 0, &out
		}
	}
	panic("unreachable")
}
func gitLabObservationHasNext(header http.Header, page int) (bool, bool) {
	value := header.Get("X-Next-Page")
	if value == "" {
		return false, true
	}
	next, err := strconv.Atoi(value)
	return true, err == nil && next == page+1
}
func classifyGitLabReviewObservationFailure(response gitLabResponse, notFound v1.ProviderReviewObservationErrorCode) v1.ProviderReviewObservationResultOutcome {
	if response.status == 408 || response.status >= 500 && response.status <= 599 {
		return reviewObservationRetryable(v1.ProviderReviewObservationProviderUnavailable, providerRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case 401:
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationInactive)
	case 403:
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationPermissionDenied)
	case 404:
		return reviewObservationPermanent(notFound)
	case 429:
		return reviewObservationRetryable(v1.ProviderReviewObservationRateLimited, providerRetryAfter(response.header, time.Now()))
	default:
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
}
