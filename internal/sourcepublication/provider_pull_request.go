package sourcepublication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var pullRequestSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type githubPullRequestRepository struct {
	ID               uint64 `json:"id"`
	FullName         string `json:"full_name"`
	AllowMergeCommit *bool  `json:"allow_merge_commit"`
	AllowSquashMerge *bool  `json:"allow_squash_merge"`
	AllowRebaseMerge *bool  `json:"allow_rebase_merge"`
}

type githubPullRequestState struct {
	Number         int64   `json:"number"`
	State          string  `json:"state"`
	Merged         bool    `json:"merged"`
	Mergeable      *bool   `json:"mergeable"`
	MergeCommitSHA *string `json:"merge_commit_sha"`
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func pullRequestFailed(code v1.ProviderPullRequestErrorCode) v1.ProviderPullRequestResultOutcome {
	return v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeFailed, Code: code}
}

func (registry *Registry) ReadyProviderPullRequestInstallations() []v1.ProviderPullRequestInstallationCapability {
	result := make([]v1.ProviderPullRequestInstallationCapability, 0, len(registry.installations))
	for id, installation := range registry.installations {
		if installation.Provider != "github" || !reviewInstallationReady(installation) {
			continue
		}
		result = append(result, v1.ProviderPullRequestInstallationCapability{InstallationID: id, Provider: installation.Provider, ProviderInstanceKey: configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InstallationID < result[j].InstallationID })
	return result
}

func (registry *Registry) RunProviderPullRequest(ctx context.Context, op v1.ProviderPullRequestAuthority, credential *v1.ProviderCredential) v1.ProviderPullRequestResultOutcome {
	installation, ok := registry.installations[op.InstallationID]
	if !ok {
		return pullRequestFailed(v1.ProviderPullRequestInstallationInactive)
	}
	if installation.Provider != op.Provider || op.Provider != "github" || configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL) != op.ProviderInstanceKey {
		return pullRequestFailed(v1.ProviderPullRequestPermissionDenied)
	}
	authority, access := resolveReviewAuthority(installation, credential, op.ProviderRepositoryID)
	switch access {
	case reviewAccessCredentialUnavailable:
		return pullRequestFailed(v1.ProviderPullRequestProviderUnavailable)
	case reviewAccessRepositoryDenied:
		return pullRequestFailed(v1.ProviderPullRequestPermissionDenied)
	case reviewAccessTokenFileInvalid:
		return pullRequestFailed(v1.ProviderPullRequestInstallationInactive)
	}
	return registry.newGitHubReviewPublisher(installation, authority).runPullRequest(ctx, op)
}

func (p *githubPublisher) runPullRequest(ctx context.Context, op v1.ProviderPullRequestAuthority) v1.ProviderPullRequestResultOutcome {
	repository, failure := p.pullRequestRepository(ctx, op.ProviderRepositoryID)
	if failure != nil {
		return *failure
	}
	owner, name, ok := githubRepository(repository.FullName)
	if !ok {
		return pullRequestFailed(v1.ProviderPullRequestRepositoryMismatch)
	}
	number, err := canonicalPositiveInt64(op.PullRequestNumber)
	if err != nil {
		return pullRequestFailed(v1.ProviderPullRequestNotFound)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls/" + strconv.FormatInt(number, 10)
	pull, failure := p.pullRequestState(ctx, path, number)
	if failure != nil {
		return *failure
	}
	allowed := allowedMergeMethods(repository)
	if op.Operation == v1.ProviderPullRequestOperationObserve {
		return observedPullRequest(pull, allowed)
	}
	if pull.Merged {
		if pull.Head.SHA == op.ExpectedHeadSHA && pull.MergeCommitSHA != nil && pullRequestSHAPattern.MatchString(*pull.MergeCommitSHA) {
			return v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeMerged, MergeCommitSHA: pull.MergeCommitSHA}
		}
		return pullRequestFailed(v1.ProviderPullRequestHeadChanged)
	}
	if pull.State != "open" {
		return pullRequestFailed(v1.ProviderPullRequestNotOpen)
	}
	if pull.Head.SHA != op.ExpectedHeadSHA {
		return pullRequestFailed(v1.ProviderPullRequestHeadChanged)
	}
	if op.MergeMethod == nil {
		return pullRequestFailed(v1.ProviderPullRequestMergeMethodNotAllowed)
	}
	if allowed != nil && !containsString(allowed, *op.MergeMethod) {
		return pullRequestFailed(v1.ProviderPullRequestMergeMethodNotAllowed)
	}
	body, err := json.Marshal(map[string]string{"sha": op.ExpectedHeadSHA, "merge_method": *op.MergeMethod})
	if err != nil {
		return pullRequestFailed(v1.ProviderPullRequestProviderUnavailable)
	}
	response, err := p.request(ctx, http.MethodPut, path+"/merge", nil, body)
	if err != nil {
		return pullRequestTransportFailure(ctx, err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyPullRequestHTTP(response.status)
	}
	var merged struct {
		Merged bool   `json:"merged"`
		SHA    string `json:"sha"`
	}
	if !reviewObservationDecode(response.body, &merged) || !merged.Merged || !pullRequestSHAPattern.MatchString(merged.SHA) {
		return pullRequestFailed(v1.ProviderPullRequestProviderUnavailable)
	}
	return v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeMerged, MergeCommitSHA: &merged.SHA}
}

func (p *githubPublisher) pullRequestRepository(ctx context.Context, providerRepositoryID string) (githubPullRequestRepository, *v1.ProviderPullRequestResultOutcome) {
	repositoryID, err := strconv.ParseUint(providerRepositoryID, 10, 64)
	if err != nil || repositoryID == 0 || strconv.FormatUint(repositoryID, 10) != providerRepositoryID {
		out := pullRequestFailed(v1.ProviderPullRequestRepositoryMismatch)
		return githubPullRequestRepository{}, &out
	}
	response, err := p.request(ctx, http.MethodGet, "/repositories/"+providerRepositoryID, nil, nil)
	if err != nil {
		out := pullRequestTransportFailure(ctx, err)
		return githubPullRequestRepository{}, &out
	}
	if response.status == http.StatusNotFound {
		out := pullRequestFailed(v1.ProviderPullRequestRepositoryMismatch)
		return githubPullRequestRepository{}, &out
	}
	if response.status < 200 || response.status >= 300 {
		out := classifyPullRequestHTTP(response.status)
		return githubPullRequestRepository{}, &out
	}
	var repository githubPullRequestRepository
	if !reviewObservationDecode(response.body, &repository) || repository.ID != repositoryID {
		out := pullRequestFailed(v1.ProviderPullRequestRepositoryMismatch)
		return githubPullRequestRepository{}, &out
	}
	return repository, nil
}

func (p *githubPublisher) pullRequestState(ctx context.Context, path string, number int64) (githubPullRequestState, *v1.ProviderPullRequestResultOutcome) {
	response, err := p.request(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		out := pullRequestTransportFailure(ctx, err)
		return githubPullRequestState{}, &out
	}
	if response.status < 200 || response.status >= 300 {
		out := classifyPullRequestHTTP(response.status)
		return githubPullRequestState{}, &out
	}
	var pull githubPullRequestState
	if !reviewObservationDecode(response.body, &pull) || pull.Number != number || !pullRequestSHAPattern.MatchString(pull.Head.SHA) || (pull.State != "open" && pull.State != "closed") {
		out := pullRequestFailed(v1.ProviderPullRequestNotFound)
		return githubPullRequestState{}, &out
	}
	return pull, nil
}

func observedPullRequest(pull githubPullRequestState, allowed []string) v1.ProviderPullRequestResultOutcome {
	state := pull.State
	var mergeCommit *string
	if pull.Merged {
		state = "merged"
		if pull.MergeCommitSHA != nil && pullRequestSHAPattern.MatchString(*pull.MergeCommitSHA) {
			mergeCommit = pull.MergeCommitSHA
		}
	}
	return v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeObserved, State: state, HeadSHA: pull.Head.SHA, MergeCommitSHA: mergeCommit, Mergeable: pull.Mergeable, AllowedMergeMethods: allowed}
}

func allowedMergeMethods(repository githubPullRequestRepository) []string {
	if repository.AllowMergeCommit == nil || repository.AllowSquashMerge == nil || repository.AllowRebaseMerge == nil {
		return nil
	}
	allowed := []string{}
	for method, enabled := range map[string]bool{"merge": *repository.AllowMergeCommit, "squash": *repository.AllowSquashMerge, "rebase": *repository.AllowRebaseMerge} {
		if enabled {
			allowed = append(allowed, method)
		}
	}
	order := map[string]int{"merge": 0, "squash": 1, "rebase": 2}
	sort.Slice(allowed, func(i, j int) bool { return order[allowed[i]] < order[allowed[j]] })
	return allowed
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func pullRequestTransportFailure(ctx context.Context, err error) v1.ProviderPullRequestResultOutcome {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return pullRequestFailed(v1.ProviderPullRequestDeadlineExceeded)
	}
	return pullRequestFailed(v1.ProviderPullRequestProviderUnavailable)
}

func classifyPullRequestHTTP(status int) v1.ProviderPullRequestResultOutcome {
	switch status {
	case http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
		return pullRequestFailed(v1.ProviderPullRequestNotMergeable)
	case http.StatusConflict:
		return pullRequestFailed(v1.ProviderPullRequestHeadChanged)
	case http.StatusUnauthorized, http.StatusForbidden:
		return pullRequestFailed(v1.ProviderPullRequestPermissionDenied)
	case http.StatusNotFound:
		return pullRequestFailed(v1.ProviderPullRequestNotFound)
	case http.StatusTooManyRequests:
		return pullRequestFailed(v1.ProviderPullRequestRateLimited)
	default:
		return pullRequestFailed(v1.ProviderPullRequestProviderUnavailable)
	}
}
