package sourcepublication

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type gitLabPublisher struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

type gitLabResponse struct {
	status int
	header http.Header
	body   []byte
}

type gitLabMergeRequest struct {
	IID          int64  `json:"iid"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
}

func newGitLabPublisher(rawBaseURL, token string, client *http.Client) *gitLabPublisher {
	baseURL, _ := url.Parse(rawBaseURL)
	return &gitLabPublisher{baseURL: baseURL, token: token, client: client}
}

func (publisher *gitLabPublisher) publish(
	ctx context.Context,
	command v1.SourcePublicationCommand,
) v1.SourcePublicationResultOutcome {
	projectPath := "/projects/" + url.PathEscape(command.RepositoryExternalID)
	response, err := publisher.request(ctx, http.MethodGet, projectPath, nil, nil)
	if err != nil {
		return publicationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitLabFailure(response, v1.SourceControlRepositoryNotFound)
	}
	var project struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(response.body, &project) != nil || project.ID <= 0 {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}

	branchPath := projectPath + "/repository/branches/" + url.PathEscape(command.HeadBranch)
	response, err = publisher.request(ctx, http.MethodGet, branchPath, nil, nil)
	if err != nil {
		return publicationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitLabFailure(response, v1.SourceControlBranchNotFound)
	}
	var branch struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if json.Unmarshal(response.body, &branch) != nil || branch.Commit.ID == "" {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	if branch.Commit.ID != command.HeadSHA {
		return permanent(v1.SourceControlHeadMismatch)
	}

	existing, found, failure := publisher.findExisting(ctx, projectPath, command)
	if failure != nil {
		return *failure
	}
	if found {
		return successfulGitLabOutcome(command, existing)
	}

	body, _ := json.Marshal(map[string]string{
		"source_branch": command.HeadBranch,
		"target_branch": command.BaseBranch,
		"title":         command.Title,
	})
	response, err = publisher.request(ctx, http.MethodPost, projectPath+"/merge_requests", nil, body)
	if err != nil {
		return publicationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		if response.status == http.StatusBadRequest || response.status == http.StatusConflict {
			existing, found, failure = publisher.findExisting(ctx, projectPath, command)
			if failure == nil && found {
				return successfulGitLabOutcome(command, existing)
			}
		}
		return classifyGitLabFailure(response, v1.SourceControlInvalidProviderResponse)
	}
	var created gitLabMergeRequest
	if json.Unmarshal(response.body, &created) != nil {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	return successfulGitLabOutcome(command, created)
}

func (publisher *gitLabPublisher) findExisting(
	ctx context.Context,
	projectPath string,
	command v1.SourcePublicationCommand,
) (gitLabMergeRequest, bool, *v1.SourcePublicationResultOutcome) {
	query := url.Values{
		"state":         {"opened"},
		"source_branch": {command.HeadBranch},
		"target_branch": {command.BaseBranch},
		"scope":         {"all"},
		"page":          {"1"},
		"per_page":      {"100"},
	}
	response, err := publisher.request(ctx, http.MethodGet, projectPath+"/merge_requests", query, nil)
	if err != nil {
		outcome := publicationRequestFailure(err)
		return gitLabMergeRequest{}, false, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitLabFailure(response, v1.SourceControlRepositoryNotFound)
		return gitLabMergeRequest{}, false, &outcome
	}
	var requests []gitLabMergeRequest
	if json.Unmarshal(response.body, &requests) != nil || len(requests) > 100 {
		outcome := permanent(v1.SourceControlInvalidProviderResponse)
		return gitLabMergeRequest{}, false, &outcome
	}
	for _, request := range requests {
		if request.SHA == command.HeadSHA && request.SourceBranch == command.HeadBranch &&
			request.TargetBranch == command.BaseBranch && request.State == "opened" {
			return request, true, nil
		}
	}
	if len(requests) != 0 {
		outcome := permanent(v1.SourceControlHeadMismatch)
		return gitLabMergeRequest{}, false, &outcome
	}
	return gitLabMergeRequest{}, false, nil
}

func (publisher *gitLabPublisher) request(
	ctx context.Context,
	method string,
	escapedPath string,
	query url.Values,
	body []byte,
) (gitLabResponse, error) {
	target, err := url.Parse(strings.TrimSuffix(publisher.baseURL.String(), "/") + escapedPath)
	if err != nil {
		return gitLabResponse{}, err
	}
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return gitLabResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("PRIVATE-TOKEN", publisher.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := publisher.client.Do(request)
	if err != nil {
		return gitLabResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if err != nil {
		return gitLabResponse{}, err
	}
	if len(responseBody) > maxProviderResponseBytes {
		return gitLabResponse{}, fmt.Errorf("%w: response exceeds limit", errInvalidProviderResponse)
	}
	return gitLabResponse{status: response.StatusCode, header: response.Header.Clone(), body: responseBody}, nil
}

func successfulGitLabOutcome(
	command v1.SourcePublicationCommand,
	request gitLabMergeRequest,
) v1.SourcePublicationResultOutcome {
	parsedURL, err := url.Parse(request.WebURL)
	if request.IID <= 0 || request.State != "opened" || request.SHA != command.HeadSHA ||
		request.SourceBranch != command.HeadBranch || request.TargetBranch != command.BaseBranch ||
		err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil ||
		parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	return v1.SourcePublicationResultOutcome{
		Kind: v1.SourcePublicationSucceeded,
		Result: &v1.SourcePublicationResult{
			SchemaVersion:        command.SchemaVersion,
			Provider:             command.Provider,
			InstallationID:       command.InstallationID,
			RepositoryID:         command.RepositoryID,
			RepositoryExternalID: command.RepositoryExternalID,
			BaseBranch:           command.BaseBranch,
			HeadBranch:           command.HeadBranch,
			HeadSHA:              command.HeadSHA,
			IdempotencyKey:       command.IdempotencyKey,
			ExternalRequestID:    strconv.FormatInt(request.IID, 10),
			URL:                  request.WebURL,
			State:                "open",
		},
	}
}

func classifyGitLabFailure(
	response gitLabResponse,
	notFoundCode v1.SourceControlErrorCode,
) v1.SourcePublicationResultOutcome {
	if response.status == http.StatusRequestTimeout || response.status >= 500 && response.status <= 599 {
		return retryable(v1.SourceControlProviderUnavailable, providerRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case http.StatusUnauthorized:
		return permanent(v1.SourceControlAuthenticationFailed)
	case http.StatusForbidden:
		return permanent(v1.SourceControlPermissionDenied)
	case http.StatusNotFound:
		return permanent(notFoundCode)
	case http.StatusTooManyRequests:
		return retryable(v1.SourceControlRateLimited, providerRetryAfter(response.header, time.Now()))
	default:
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
}
