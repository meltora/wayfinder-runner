package sourcepublication

import (
	"context"
	v1 "github.com/example/app-runner/api/runner/v1"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type githubReviewObservationRequest struct {
	Number         int64     `json:"number"`
	State          string    `json:"state"`
	Merged         bool      `json:"merged"`
	MergeCommitSHA string    `json:"merge_commit_sha"`
	MergedAt       time.Time `json:"merged_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	MergedBy *struct {
		ID uint64 `json:"id"`
	} `json:"merged_by"`
}

func (p *githubPublisher) observeProviderReviews(ctx context.Context, op v1.ProviderReviewObservationOperation) v1.ProviderReviewObservationResultOutcome {
	repoID, err := strconv.ParseUint(op.ProviderRepositoryID, 10, 64)
	if err != nil || repoID == 0 || strconv.FormatUint(repoID, 10) != op.ProviderRepositoryID {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	response, err := p.request(ctx, http.MethodGet, "/repositories/"+op.ProviderRepositoryID, nil, nil)
	if err != nil {
		return reviewObservationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitHubReviewObservationFailure(response, v1.ProviderReviewObservationRepositoryMismatch)
	}
	var repo struct {
		ID       uint64 `json:"id"`
		FullName string `json:"full_name"`
	}
	if !reviewObservationDecode(response.body, &repo) || repo.ID != repoID {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	owner, name, ok := githubRepository(repo.FullName)
	if !ok {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	number, err := canonicalPositiveInt64(op.ExternalRequestID)
	if err != nil {
		return reviewObservationPermanent(v1.ProviderReviewObservationRequestNotFound)
	}
	requestPath := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls/" + strconv.FormatInt(number, 10)
	response, err = p.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		return reviewObservationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitHubReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
	}
	var request githubReviewObservationRequest
	if !reviewObservationDecode(response.body, &request) || request.Number != number || request.Head.SHA == "" {
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
	state := v1.ProviderReviewObservationRequestState("")
	if request.Merged {
		state = v1.ProviderReviewObservationRequestMerged
	} else if request.State == "open" {
		state = v1.ProviderReviewObservationRequestOpen
	} else if request.State == "closed" {
		state = v1.ProviderReviewObservationRequestClosed
	} else {
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
	var completion *providerReviewCompletionEvidence
	if providerReviewCompletionRequested(op) {
		if state != v1.ProviderReviewObservationRequestMerged {
			return reviewObservationPermanent(v1.ProviderReviewObservationIncomplete)
		}
		evidence, failure := p.githubReviewCompletionEvidence(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), op.ExpectedTargetBranch, request)
		if failure != nil {
			return *failure
		}
		completion = &evidence
	}
	reviews, reviewPages, failure := p.githubReviewEvents(ctx, requestPath)
	if failure != nil {
		return *failure
	}
	comments, commentPages, failure := p.githubComments(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), requestPath, number)
	if failure != nil {
		return *failure
	}
	if completion != nil {
		return reviewObservationCompletionSuccess(op, request.Head.SHA, reviewPages, commentPages, reviews, comments, *completion)
	}
	return reviewObservationSuccess(op, state, request.Head.SHA, reviewPages, commentPages, reviews, comments)
}

func (p *githubPublisher) githubReviewCompletionEvidence(ctx context.Context, repositoryPath, expectedTargetBranch string, request githubReviewObservationRequest) (providerReviewCompletionEvidence, *v1.ProviderReviewObservationResultOutcome) {
	invalid := func() (providerReviewCompletionEvidence, *v1.ProviderReviewObservationResultOutcome) {
		outcome := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return providerReviewCompletionEvidence{}, &outcome
	}
	if !request.Merged || !validProviderReviewCommitSHA(request.MergeCommitSHA) ||
		!validProviderReviewTargetBranch(request.Base.Ref) || !validProviderReviewTargetBranch(expectedTargetBranch) || request.MergedAt.IsZero() ||
		request.UpdatedAt.IsZero() || request.MergedBy == nil || request.MergedBy.ID == 0 {
		return invalid()
	}
	if request.Base.Ref != expectedTargetBranch {
		outcome := reviewObservationPermanent(v1.ProviderReviewObservationIncomplete)
		return providerReviewCompletionEvidence{}, &outcome
	}
	response, err := p.request(ctx, http.MethodGet, repositoryPath+"/git/ref/heads/"+request.Base.Ref, nil, nil)
	if err != nil {
		outcome := reviewObservationRequestFailure(err)
		return providerReviewCompletionEvidence{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitHubReviewObservationFailure(response, v1.ProviderReviewObservationIncomplete)
		return providerReviewCompletionEvidence{}, &outcome
	}
	var target struct {
		Ref    string `json:"ref"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if !reviewObservationDecode(response.body, &target) || target.Ref != "refs/heads/"+request.Base.Ref ||
		target.Object.Type != "commit" || !validProviderReviewCommitSHA(target.Object.SHA) {
		return invalid()
	}
	reachable, failure := p.githubMergeCommitReachableFromTarget(ctx, repositoryPath, request.MergeCommitSHA, target.Object.SHA)
	if failure != nil {
		return providerReviewCompletionEvidence{}, failure
	}
	evidence := providerReviewCompletionEvidence{
		mergeCommitSHA: request.MergeCommitSHA, targetBranch: request.Base.Ref, targetHeadSHA: target.Object.SHA,
		mergeCommitReachableFromTarget: reachable, mergedAt: request.MergedAt, providerUpdatedAt: request.UpdatedAt,
		mergedByProviderAccountID: strconv.FormatUint(request.MergedBy.ID, 10),
	}
	if !validProviderReviewCompletionEvidence(evidence, time.Now().UTC()) {
		return invalid()
	}
	return evidence, nil
}

func (p *githubPublisher) githubMergeCommitReachableFromTarget(ctx context.Context, repositoryPath, mergeCommitSHA, targetHeadSHA string) (bool, *v1.ProviderReviewObservationResultOutcome) {
	if !validProviderReviewCommitSHA(mergeCommitSHA) || !validProviderReviewCommitSHA(targetHeadSHA) {
		outcome := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return false, &outcome
	}
	response, err := p.request(ctx, http.MethodGet, repositoryPath+"/compare/"+mergeCommitSHA+"..."+targetHeadSHA, nil, nil)
	if err != nil {
		outcome := reviewObservationRequestFailure(err)
		return false, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitHubReviewObservationFailure(response, v1.ProviderReviewObservationIncomplete)
		return false, &outcome
	}
	var comparison struct {
		Status     string `json:"status"`
		BaseCommit struct {
			SHA string `json:"sha"`
		} `json:"base_commit"`
		MergeBaseCommit struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if !reviewObservationDecode(response.body, &comparison) || comparison.BaseCommit.SHA != mergeCommitSHA {
		outcome := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return false, &outcome
	}
	reachable := comparison.MergeBaseCommit.SHA == mergeCommitSHA
	valid := false
	switch comparison.Status {
	case "identical":
		valid = reachable && mergeCommitSHA == targetHeadSHA
	case "ahead":
		valid = reachable && mergeCommitSHA != targetHeadSHA
	case "behind", "diverged":
		valid = !reachable
	}
	if !valid {
		outcome := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return false, &outcome
	}
	return reachable, nil
}

func (p *githubPublisher) githubReviewEvents(ctx context.Context, path string) ([]v1.ProviderReviewObservationReviewEvent, int, *v1.ProviderReviewObservationResultOutcome) {
	result := []v1.ProviderReviewObservationReviewEvent{}
	seen := map[uint64]struct{}{}
	for page := 1; page <= 10; page++ {
		response, err := p.request(ctx, http.MethodGet, path+"/reviews", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			out := reviewObservationRequestFailure(err)
			return nil, 0, &out
		}
		if response.status < 200 || response.status >= 300 {
			out := classifyGitHubReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
			return nil, 0, &out
		}
		var values []struct {
			ID          uint64                `json:"id"`
			State       string                `json:"state"`
			Body        *string               `json:"body"`
			SubmittedAt time.Time             `json:"submitted_at"`
			CommitID    string                `json:"commit_id"`
			User        githubReviewerAccount `json:"user"`
		}
		if !reviewObservationDecode(response.body, &values) || len(values) > 100 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, 0, &out
		}
		for _, item := range values {
			if item.ID == 0 || item.SubmittedAt.IsZero() || item.Body == nil || item.User.ID == 0 || item.User.Type != "User" {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			if _, dup := seen[item.ID]; dup {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			seen[item.ID] = struct{}{}
			state, kind, ok := githubReviewState(item.State)
			if !ok {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			digest, count, ok := reviewObservationContent(*item.Body)
			if !ok {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			result = append(result, v1.ProviderReviewObservationReviewEvent{ProviderReviewEventID: strconv.FormatUint(item.ID, 10), ProviderReviewEventRevision: 1, ProviderAccountID: strconv.FormatUint(item.User.ID, 10), ReviewEventKind: kind, ReviewState: state, OccurredAt: item.SubmittedAt, CommitSHA: item.CommitID, ContentDigest: digest, ContentByteCount: count})
		}
		hasNext := strings.Contains(response.header.Get("Link"), `rel="next"`)
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
func githubReviewState(state string) (v1.ProviderReviewObservationReviewState, v1.ProviderReviewObservationReviewEventKind, bool) {
	switch state {
	case "PENDING":
		return v1.ProviderReviewObservationUnreviewed, v1.ProviderReviewObservationReview, true
	case "COMMENTED":
		return v1.ProviderReviewObservationCommented, v1.ProviderReviewObservationReview, true
	case "APPROVED":
		return v1.ProviderReviewObservationApproved, v1.ProviderReviewObservationApproval, true
	case "CHANGES_REQUESTED":
		return v1.ProviderReviewObservationChangesRequested, v1.ProviderReviewObservationChangeRequest, true
	case "DISMISSED":
		return v1.ProviderReviewObservationDismissed, v1.ProviderReviewObservationDismissal, true
	}
	return "", "", false
}

func (p *githubPublisher) githubComments(ctx context.Context, repoPath, requestPath string, number int64) ([]v1.ProviderReviewObservationComment, int, *v1.ProviderReviewObservationResultOutcome) {
	result := []v1.ProviderReviewObservationComment{}
	issueSeen := map[uint64]struct{}{}
	reviewSeen := map[uint64]struct{}{}
	pagesRead := 1
	for page := 1; page <= 10; page++ {
		issue, issueFull, failure := p.githubIssueCommentPage(ctx, repoPath+"/issues/"+strconv.FormatInt(number, 10)+"/comments", page, issueSeen)
		if failure != nil {
			return nil, 0, failure
		}
		review, reviewFull, failure := p.githubReviewCommentPage(ctx, requestPath+"/comments", page, reviewSeen)
		if failure != nil {
			return nil, 0, failure
		}
		result = append(result, issue...)
		result = append(result, review...)
		pagesRead = page
		if !issueFull && !reviewFull {
			break
		}
		if page == 10 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationIncomplete)
			return nil, 0, &out
		}
	}
	return result, pagesRead, nil
}
func (p *githubPublisher) githubIssueCommentPage(ctx context.Context, path string, page int, seen map[uint64]struct{}) ([]v1.ProviderReviewObservationComment, bool, *v1.ProviderReviewObservationResultOutcome) {
	response, err := p.request(ctx, http.MethodGet, path, url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}, nil)
	if err != nil {
		out := reviewObservationRequestFailure(err)
		return nil, false, &out
	}
	if response.status < 200 || response.status >= 300 {
		out := classifyGitHubReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
		return nil, false, &out
	}
	var values []struct {
		ID        uint64                `json:"id"`
		Body      *string               `json:"body"`
		CreatedAt time.Time             `json:"created_at"`
		UpdatedAt time.Time             `json:"updated_at"`
		User      githubReviewerAccount `json:"user"`
	}
	if !reviewObservationDecode(response.body, &values) || len(values) > 50 {
		out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return nil, false, &out
	}
	result := make([]v1.ProviderReviewObservationComment, 0, len(values))
	for _, item := range values {
		comment, ok := githubNormalizedComment(item.ID, item.Body, item.CreatedAt, item.UpdatedAt, item.User, v1.ProviderReviewObservationIssue, v1.ProviderReviewObservationGeneral, "", nil)
		if !ok {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, false, &out
		}
		if _, dup := seen[item.ID]; dup {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, false, &out
		}
		seen[item.ID] = struct{}{}
		result = append(result, comment)
	}
	hasNext := strings.Contains(response.header.Get("Link"), `rel="next"`)
	if hasNext && len(values) == 0 {
		out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return nil, false, &out
	}
	return result, hasNext, nil
}
func (p *githubPublisher) githubReviewCommentPage(ctx context.Context, path string, page int, seen map[uint64]struct{}) ([]v1.ProviderReviewObservationComment, bool, *v1.ProviderReviewObservationResultOutcome) {
	response, err := p.request(ctx, http.MethodGet, path, url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}, nil)
	if err != nil {
		out := reviewObservationRequestFailure(err)
		return nil, false, &out
	}
	if response.status < 200 || response.status >= 300 {
		out := classifyGitHubReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
		return nil, false, &out
	}
	var values []struct {
		ID           uint64                `json:"id"`
		Body         *string               `json:"body"`
		CreatedAt    time.Time             `json:"created_at"`
		UpdatedAt    time.Time             `json:"updated_at"`
		InReplyToID  uint64                `json:"in_reply_to_id"`
		Path         string                `json:"path"`
		Line         uint64                `json:"line"`
		OriginalLine uint64                `json:"original_line"`
		Side         string                `json:"side"`
		CommitID     string                `json:"commit_id"`
		User         githubReviewerAccount `json:"user"`
	}
	if !reviewObservationDecode(response.body, &values) || len(values) > 50 {
		out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return nil, false, &out
	}
	result := make([]v1.ProviderReviewObservationComment, 0, len(values))
	for _, item := range values {
		kind := v1.ProviderReviewObservationInline
		parent := ""
		var position *v1.ProviderReviewObservationCommentPosition
		if item.InReplyToID != 0 {
			kind = v1.ProviderReviewObservationThreadReply
			parent = strconv.FormatUint(item.InReplyToID, 10)
		} else {
			line := item.Line
			if line == 0 {
				line = item.OriginalLine
			}
			side := strings.ToLower(item.Side)
			position = &v1.ProviderReviewObservationCommentPosition{FilePath: item.Path, Line: line, Side: side, CommitSHA: item.CommitID}
		}
		comment, ok := githubNormalizedComment(item.ID, item.Body, item.CreatedAt, item.UpdatedAt, item.User, v1.ProviderReviewObservationReviewNamespace, kind, parent, position)
		if !ok {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, false, &out
		}
		if _, dup := seen[item.ID]; dup {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, false, &out
		}
		seen[item.ID] = struct{}{}
		result = append(result, comment)
	}
	hasNext := strings.Contains(response.header.Get("Link"), `rel="next"`)
	if hasNext && len(values) == 0 {
		out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
		return nil, false, &out
	}
	return result, hasNext, nil
}
func githubNormalizedComment(id uint64, body *string, created, updated time.Time, user githubReviewerAccount, namespace v1.ProviderReviewObservationCommentNamespace, kind v1.ProviderReviewObservationCommentKind, parent string, position *v1.ProviderReviewObservationCommentPosition) (v1.ProviderReviewObservationComment, bool) {
	if id == 0 || body == nil || created.IsZero() || updated.IsZero() || user.ID == 0 || user.Type != "User" {
		return v1.ProviderReviewObservationComment{}, false
	}
	digest, count, ok := reviewObservationContent(*body)
	if !ok {
		return v1.ProviderReviewObservationComment{}, false
	}
	revision := updated.UnixMilli()
	if revision <= 0 {
		return v1.ProviderReviewObservationComment{}, false
	}
	return v1.ProviderReviewObservationComment{ProviderCommentID: strconv.FormatUint(id, 10), ProviderCommentRevision: uint64(revision), ProviderAccountID: strconv.FormatUint(user.ID, 10), ProviderCommentNamespace: namespace, CommentKind: kind, CreatedAt: created, UpdatedAt: updated, ParentCommentID: parent, Position: position, Resolved: false, Deleted: false, ContentDigest: digest, ContentByteCount: count}, true
}
func classifyGitHubReviewObservationFailure(response githubResponse, notFound v1.ProviderReviewObservationErrorCode) v1.ProviderReviewObservationResultOutcome {
	if response.status == 408 || response.status >= 500 && response.status <= 599 {
		return reviewObservationRetryable(v1.ProviderReviewObservationProviderUnavailable, providerRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case 401:
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationInactive)
	case 403:
		if response.header.Get("X-RateLimit-Remaining") == "0" || response.header.Get("Retry-After") != "" {
			return reviewObservationRetryable(v1.ProviderReviewObservationRateLimited, providerRetryAfter(response.header, time.Now()))
		}
		return reviewObservationPermanent(v1.ProviderReviewObservationInstallationPermissionDenied)
	case 404:
		return reviewObservationPermanent(notFound)
	case 429:
		return reviewObservationRetryable(v1.ProviderReviewObservationRateLimited, providerRetryAfter(response.header, time.Now()))
	default:
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
}
