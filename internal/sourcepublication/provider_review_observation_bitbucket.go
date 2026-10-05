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

func (p *bitbucketPublisher) observeProviderReviews(ctx context.Context, op v1.ProviderReviewObservationOperation) v1.ProviderReviewObservationResultOutcome {
	if providerReviewCompletionRequested(op) {
		return reviewObservationPermanent(v1.ProviderReviewObservationUnsupportedProviderInstance)
	}
	workspace, name, ok := bitbucketRepository(op.ProviderRepositoryPath)
	if !ok || !bitbucketUUIDPattern.MatchString(op.ProviderRepositoryID) {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	repoPath := "/repositories/" + url.PathEscape(workspace) + "/" + url.PathEscape(name)
	response, err := p.request(ctx, http.MethodGet, repoPath, nil, nil)
	if err != nil {
		return reviewObservationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyBitbucketReviewObservationFailure(response, v1.ProviderReviewObservationRepositoryMismatch)
	}
	var repo struct {
		UUID     string `json:"uuid"`
		FullName string `json:"full_name"`
	}
	if !reviewObservationDecode(response.body, &repo) || repo.UUID != op.ProviderRepositoryID || repo.FullName != op.ProviderRepositoryPath {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	id, err := canonicalPositiveInt64(op.ExternalRequestID)
	if err != nil {
		return reviewObservationPermanent(v1.ProviderReviewObservationRequestNotFound)
	}
	requestPath := repoPath + "/pullrequests/" + strconv.FormatInt(id, 10)
	response, err = p.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		return reviewObservationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyBitbucketReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
	}
	var request struct {
		ID           int64                        `json:"id"`
		State        string                       `json:"state"`
		UpdatedOn    time.Time                    `json:"updated_on"`
		Source       bitbucketPullRequestEndpoint `json:"source"`
		Destination  bitbucketPullRequestEndpoint `json:"destination"`
		Reviewers    *[]bitbucketReviewerAccount  `json:"reviewers"`
		Participants *[]struct {
			bitbucketReviewerAccount
			Role     string `json:"role"`
			Approved bool   `json:"approved"`
			State    string `json:"state"`
		} `json:"participants"`
	}
	if !reviewObservationDecode(response.body, &request) || request.ID != id || request.Source.Commit.Hash == "" || request.UpdatedOn.IsZero() || request.Reviewers == nil || request.Participants == nil || len(*request.Reviewers) > 100 || len(*request.Participants) > 100 {
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
	if request.Source.Repository.FullName != op.ProviderRepositoryPath || request.Destination.Repository.FullName != op.ProviderRepositoryPath {
		return reviewObservationPermanent(v1.ProviderReviewObservationRepositoryMismatch)
	}
	state := v1.ProviderReviewObservationRequestState("")
	switch request.State {
	case "OPEN":
		state = v1.ProviderReviewObservationRequestOpen
	case "DECLINED", "SUPERSEDED":
		state = v1.ProviderReviewObservationRequestClosed
	case "MERGED":
		state = v1.ProviderReviewObservationRequestMerged
	default:
		return reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
	}
	reviews, failure := bitbucketReviewEvents(op, request.UpdatedOn, *request.Reviewers, *request.Participants)
	if failure != nil {
		return *failure
	}
	comments, pages, failure := p.bitbucketReviewComments(ctx, requestPath, request.Source.Commit.Hash)
	if failure != nil {
		return *failure
	}
	return reviewObservationSuccess(op, state, request.Source.Commit.Hash, 1, pages, reviews, comments)
}

type bitbucketObservationParticipant struct {
	bitbucketReviewerAccount
	Role     string
	Approved bool
	State    string
}

func bitbucketReviewEvents(op v1.ProviderReviewObservationOperation, occurred time.Time, reviewers []bitbucketReviewerAccount, rawParticipants []struct {
	bitbucketReviewerAccount
	Role     string `json:"role"`
	Approved bool   `json:"approved"`
	State    string `json:"state"`
}) ([]v1.ProviderReviewObservationReviewEvent, *v1.ProviderReviewObservationResultOutcome) {
	participants := make(map[string]bitbucketObservationParticipant, len(rawParticipants))
	accounts, uuids := map[string]string{}, map[string]string{}
	for _, item := range rawParticipants {
		if !validBitbucketObservedAccount(item.bitbucketReviewerAccount) || bitbucketObservedAccountConflicts(item.bitbucketReviewerAccount, accounts, uuids) {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, &out
		}
		if _, exists := participants[item.AccountID]; exists {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, &out
		}
		participants[item.AccountID] = bitbucketObservationParticipant{item.bitbucketReviewerAccount, item.Role, item.Approved, item.State}
	}
	emptyDigest, emptyCount := reviewObservationEmptyContent()
	result := make([]v1.ProviderReviewObservationReviewEvent, 0, len(reviewers))
	seen := map[string]struct{}{}
	for _, reviewer := range reviewers {
		if !validBitbucketObservedAccount(reviewer) || bitbucketObservedAccountConflicts(reviewer, accounts, uuids) {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, &out
		}
		if _, dup := seen[reviewer.AccountID]; dup {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, &out
		}
		seen[reviewer.AccountID] = struct{}{}
		state, kind := v1.ProviderReviewObservationUnreviewed, v1.ProviderReviewObservationReviewerState
		if participant, exists := participants[reviewer.AccountID]; exists {
			switch {
			case participant.Approved || participant.State == "approved":
				state, kind = v1.ProviderReviewObservationApproved, v1.ProviderReviewObservationApproval
			case participant.State == "changes_requested":
				state, kind = v1.ProviderReviewObservationChangesRequested, v1.ProviderReviewObservationChangeRequest
			case participant.State == "participating" || participant.State == "":
			default:
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, &out
			}
		}
		id := v1.ProviderReviewObservationDerivedReviewEventID(v1.ProviderReviewObservationDerivedReviewEventIdentity{Provider: op.Provider, ProviderInstanceKey: op.ProviderInstanceKey, ProviderRepositoryID: op.ProviderRepositoryID, ExternalRequestID: op.ExternalRequestID, ReviewEventKind: kind, ProviderAccountID: reviewer.AccountID, OccurredAt: occurred})
		result = append(result, v1.ProviderReviewObservationReviewEvent{ProviderReviewEventID: id, ProviderReviewEventRevision: uint64(occurred.UnixMilli()), ProviderAccountID: reviewer.AccountID, ReviewEventKind: kind, ReviewState: state, OccurredAt: occurred, ContentDigest: emptyDigest, ContentByteCount: emptyCount})
	}
	return result, nil
}
func (p *bitbucketPublisher) bitbucketReviewComments(ctx context.Context, path, headSHA string) ([]v1.ProviderReviewObservationComment, int, *v1.ProviderReviewObservationResultOutcome) {
	result := []v1.ProviderReviewObservationComment{}
	seen := map[uint64]struct{}{}
	accounts, uuids := map[string]string{}, map[string]string{}
	for page := 1; page <= 10; page++ {
		response, err := p.request(ctx, http.MethodGet, path+"/comments", url.Values{"pagelen": {"100"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			out := reviewObservationRequestFailure(err)
			return nil, 0, &out
		}
		if response.status < 200 || response.status >= 300 {
			out := classifyBitbucketReviewObservationFailure(response, v1.ProviderReviewObservationRequestNotFound)
			return nil, 0, &out
		}
		var pageBody struct {
			Next   string `json:"next"`
			Values *[]struct {
				ID     uint64 `json:"id"`
				Parent *struct {
					ID uint64 `json:"id"`
				} `json:"parent"`
				Content *struct {
					Raw string `json:"raw"`
				} `json:"content"`
				CreatedOn time.Time                `json:"created_on"`
				UpdatedOn time.Time                `json:"updated_on"`
				Deleted   bool                     `json:"deleted"`
				User      bitbucketReviewerAccount `json:"user"`
				Inline    *struct {
					Path string `json:"path"`
					From uint64 `json:"from"`
					To   uint64 `json:"to"`
				} `json:"inline"`
			} `json:"values"`
		}
		if !reviewObservationDecode(response.body, &pageBody) || pageBody.Values == nil || len(*pageBody.Values) > 100 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
			return nil, 0, &out
		}
		for _, item := range *pageBody.Values {
			if item.ID == 0 || item.Content == nil || item.CreatedOn.IsZero() || item.UpdatedOn.IsZero() || !validBitbucketObservedAccount(item.User) || bitbucketObservedAccountConflicts(item.User, accounts, uuids) {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			if _, dup := seen[item.ID]; dup {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			seen[item.ID] = struct{}{}
			digest, count, ok := reviewObservationContent(item.Content.Raw)
			if !ok {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			kind := v1.ProviderReviewObservationGeneral
			parent, thread := "", ""
			var position *v1.ProviderReviewObservationCommentPosition
			if item.Parent != nil {
				if item.Parent.ID == 0 {
					out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
					return nil, 0, &out
				}
				kind = v1.ProviderReviewObservationThreadReply
				parent = strconv.FormatUint(item.Parent.ID, 10)
				thread = parent
			} else if item.Inline != nil {
				line, side := item.Inline.To, "right"
				if line == 0 {
					line, side = item.Inline.From, "left"
				}
				kind = v1.ProviderReviewObservationInline
				position = &v1.ProviderReviewObservationCommentPosition{FilePath: item.Inline.Path, Line: line, Side: side, CommitSHA: headSHA}
			}
			revision := item.UpdatedOn.UnixMilli()
			if revision <= 0 {
				out := reviewObservationPermanent(v1.ProviderReviewObservationInvalidProviderResponse)
				return nil, 0, &out
			}
			result = append(result, v1.ProviderReviewObservationComment{ProviderCommentID: strconv.FormatUint(item.ID, 10), ProviderCommentRevision: uint64(revision), ProviderAccountID: item.User.AccountID, ProviderCommentNamespace: v1.ProviderReviewObservationPullRequest, CommentKind: kind, CreatedAt: item.CreatedOn, UpdatedAt: item.UpdatedOn, ThreadID: thread, ParentCommentID: parent, Position: position, Resolved: false, Deleted: item.Deleted, ContentDigest: digest, ContentByteCount: count})
		}
		if pageBody.Next == "" {
			return result, page, nil
		}
		if !p.bitbucketObservationNextValid(pageBody.Next, path+"/comments", page) || len(*pageBody.Values) == 0 || page == 10 {
			out := reviewObservationPermanent(v1.ProviderReviewObservationIncomplete)
			return nil, 0, &out
		}
	}
	panic("unreachable")
}
func (p *bitbucketPublisher) bitbucketObservationNextValid(raw, path string, page int) bool {
	next, err := url.Parse(raw)
	if err != nil || next.Scheme != "https" && next.Scheme != "http" || next.Host != p.baseURL.Host || next.Path != strings.TrimSuffix(p.baseURL.Path, "/")+path {
		return false
	}
	number, err := strconv.Atoi(next.Query().Get("page"))
	return err == nil && number == page+1 && next.Query().Get("pagelen") == "100"
}
func classifyBitbucketReviewObservationFailure(response bitbucketResponse, notFound v1.ProviderReviewObservationErrorCode) v1.ProviderReviewObservationResultOutcome {
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
