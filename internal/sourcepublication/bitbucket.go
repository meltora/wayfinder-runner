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

const bitbucketPageLength = 50

type bitbucketPublisher struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

type bitbucketResponse struct {
	status int
	header http.Header
	body   []byte
}

type bitbucketPullRequest struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
	Source      bitbucketPullRequestEndpoint `json:"source"`
	Destination bitbucketPullRequestEndpoint `json:"destination"`
}

type bitbucketPullRequestEndpoint struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type bitbucketPullRequestPage struct {
	Next   string                  `json:"next"`
	Values *[]bitbucketPullRequest `json:"values"`
}

func newBitbucketPublisher(rawBaseURL, token string, client *http.Client) *bitbucketPublisher {
	baseURL, _ := url.Parse(rawBaseURL)
	return &bitbucketPublisher{baseURL: baseURL, token: token, client: client}
}

func (publisher *bitbucketPublisher) publish(
	ctx context.Context,
	command v1.SourcePublicationCommand,
) v1.SourcePublicationResultOutcome {
	workspace, repository, ok := bitbucketRepository(command.RepositoryExternalID)
	if !ok {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	repositoryPath := "/repositories/" + url.PathEscape(workspace) + "/" + url.PathEscape(repository)
	response, err := publisher.request(ctx, http.MethodGet, repositoryPath, nil, nil)
	if err != nil {
		return publicationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyBitbucketFailure(response, v1.SourceControlRepositoryNotFound)
	}
	var repositoryResponse struct {
		UUID     string `json:"uuid"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(response.body, &repositoryResponse) != nil || repositoryResponse.UUID == "" ||
		repositoryResponse.FullName != command.RepositoryExternalID {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}

	branchPath := repositoryPath + "/refs/branches/" + url.PathEscape(command.HeadBranch)
	response, err = publisher.request(ctx, http.MethodGet, branchPath, nil, nil)
	if err != nil {
		return publicationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyBitbucketFailure(response, v1.SourceControlBranchNotFound)
	}
	var branch struct {
		Name   string `json:"name"`
		Target struct {
			Hash string `json:"hash"`
		} `json:"target"`
	}
	if json.Unmarshal(response.body, &branch) != nil || branch.Name != command.HeadBranch || branch.Target.Hash == "" {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	if branch.Target.Hash != command.HeadSHA {
		return permanent(v1.SourceControlHeadMismatch)
	}

	existing, found, failure := publisher.findExisting(ctx, repositoryPath, command)
	if failure != nil {
		return *failure
	}
	if found {
		return successfulBitbucketOutcome(command, existing)
	}

	body, _ := json.Marshal(map[string]any{
		"title":       command.Title,
		"source":      map[string]any{"branch": map[string]string{"name": command.HeadBranch}},
		"destination": map[string]any{"branch": map[string]string{"name": command.BaseBranch}},
	})
	response, err = publisher.request(ctx, http.MethodPost, repositoryPath+"/pullrequests", nil, body)
	if err != nil {
		return publicationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		if response.status == http.StatusBadRequest || response.status == http.StatusConflict {
			existing, found, failure = publisher.findExisting(ctx, repositoryPath, command)
			if failure == nil && found {
				return successfulBitbucketOutcome(command, existing)
			}
		}
		return classifyBitbucketFailure(response, v1.SourceControlInvalidProviderResponse)
	}
	var created bitbucketPullRequest
	if json.Unmarshal(response.body, &created) != nil {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	return successfulBitbucketOutcome(command, created)
}

func (publisher *bitbucketPublisher) findExisting(
	ctx context.Context,
	repositoryPath string,
	command v1.SourcePublicationCommand,
) (bitbucketPullRequest, bool, *v1.SourcePublicationResultOutcome) {
	filter := bitbucketPullRequestFilter(command)
	query := url.Values{"q": {filter}, "pagelen": {strconv.Itoa(bitbucketPageLength)}}
	response, err := publisher.request(ctx, http.MethodGet, repositoryPath+"/pullrequests", query, nil)
	if err != nil {
		outcome := publicationRequestFailure(err)
		return bitbucketPullRequest{}, false, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyBitbucketFailure(response, v1.SourceControlRepositoryNotFound)
		return bitbucketPullRequest{}, false, &outcome
	}
	var page bitbucketPullRequestPage
	if json.Unmarshal(response.body, &page) != nil || page.Values == nil || page.Next != "" ||
		len(*page.Values) > bitbucketPageLength {
		outcome := permanent(v1.SourceControlInvalidProviderResponse)
		return bitbucketPullRequest{}, false, &outcome
	}
	for _, request := range *page.Values {
		if bitbucketRequestMatches(command, request) {
			return request, true, nil
		}
	}
	if len(*page.Values) != 0 {
		outcome := permanent(v1.SourceControlHeadMismatch)
		return bitbucketPullRequest{}, false, &outcome
	}
	return bitbucketPullRequest{}, false, nil
}

func bitbucketPullRequestFilter(command v1.SourcePublicationCommand) string {
	return "state = \"OPEN\" AND source.repository.full_name = " + strconv.Quote(command.RepositoryExternalID) +
		" AND source.branch.name = " + strconv.Quote(command.HeadBranch) +
		" AND destination.branch.name = " + strconv.Quote(command.BaseBranch)
}

func bitbucketRequestMatches(command v1.SourcePublicationCommand, request bitbucketPullRequest) bool {
	return request.State == "OPEN" && request.Source.Branch.Name == command.HeadBranch &&
		request.Source.Commit.Hash == command.HeadSHA &&
		request.Source.Repository.FullName == command.RepositoryExternalID &&
		request.Destination.Branch.Name == command.BaseBranch &&
		request.Destination.Repository.FullName == command.RepositoryExternalID
}

func (publisher *bitbucketPublisher) request(
	ctx context.Context,
	method string,
	escapedPath string,
	query url.Values,
	body []byte,
) (bitbucketResponse, error) {
	target, err := url.Parse(strings.TrimSuffix(publisher.baseURL.String(), "/") + escapedPath)
	if err != nil {
		return bitbucketResponse{}, err
	}
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return bitbucketResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+publisher.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := publisher.client.Do(request)
	if err != nil {
		return bitbucketResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if err != nil {
		return bitbucketResponse{}, err
	}
	if len(responseBody) > maxProviderResponseBytes {
		return bitbucketResponse{}, fmt.Errorf("%w: response exceeds limit", errInvalidProviderResponse)
	}
	return bitbucketResponse{status: response.StatusCode, header: response.Header.Clone(), body: responseBody}, nil
}

func bitbucketRepository(value string) (string, string, bool) {
	return githubRepository(value)
}

func successfulBitbucketOutcome(
	command v1.SourcePublicationCommand,
	request bitbucketPullRequest,
) v1.SourcePublicationResultOutcome {
	parsedURL, err := url.Parse(request.Links.HTML.Href)
	if request.ID <= 0 || !bitbucketRequestMatches(command, request) || err != nil ||
		parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil ||
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
			ExternalRequestID:    strconv.FormatInt(request.ID, 10),
			URL:                  request.Links.HTML.Href,
			State:                "open",
		},
	}
}

func classifyBitbucketFailure(
	response bitbucketResponse,
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
