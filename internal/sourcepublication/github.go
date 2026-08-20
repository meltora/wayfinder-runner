package sourcepublication

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxProviderResponseBytes = 1 << 20

var githubRepositoryComponentPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})$`)

type githubPublisher struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

type githubResponse struct {
	status int
	header http.Header
	body   []byte
}

type githubPullRequest struct {
	Number  int64  `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Head    struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func newGitHubPublisher(rawBaseURL, token string, client *http.Client) *githubPublisher {
	baseURL, _ := url.Parse(rawBaseURL)
	return &githubPublisher{baseURL: baseURL, token: token, client: client}
}

func (publisher *githubPublisher) publish(
	ctx context.Context,
	command v1.SourcePublicationCommand,
) v1.SourcePublicationResultOutcome {
	owner, repository, ok := githubRepository(command.RepositoryExternalID)
	if !ok {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	repositoryPath := "/repos/" + owner + "/" + repository
	response, err := publisher.request(ctx, http.MethodGet, repositoryPath, nil, nil)
	if err != nil {
		return retryable(v1.SourceControlProviderUnavailable, nil)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitHubFailure(response, v1.SourceControlRepositoryNotFound)
	}

	response, err = publisher.request(ctx, http.MethodGet, repositoryPath+"/git/ref/heads/"+command.HeadBranch, nil, nil)
	if err != nil {
		return retryable(v1.SourceControlProviderUnavailable, nil)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitHubFailure(response, v1.SourceControlBranchNotFound)
	}
	var reference struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if json.Unmarshal(response.body, &reference) != nil || reference.Object.SHA == "" {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	if reference.Object.SHA != command.HeadSHA {
		return permanent(v1.SourceControlHeadMismatch)
	}

	existing, found, failure := publisher.findExisting(ctx, repositoryPath, owner, command)
	if failure != nil {
		return *failure
	}
	if found {
		return successfulGitHubOutcome(command, existing)
	}

	body, _ := json.Marshal(map[string]string{
		"title": command.Title, "head": command.HeadBranch, "base": command.BaseBranch,
	})
	response, err = publisher.request(ctx, http.MethodPost, repositoryPath+"/pulls", nil, body)
	if err != nil {
		return retryable(v1.SourceControlProviderUnavailable, nil)
	}
	if response.status < 200 || response.status >= 300 {
		if response.status == http.StatusConflict || response.status == http.StatusUnprocessableEntity {
			existing, found, failure = publisher.findExisting(ctx, repositoryPath, owner, command)
			if failure == nil && found {
				return successfulGitHubOutcome(command, existing)
			}
		}
		return classifyGitHubFailure(response, v1.SourceControlInvalidProviderResponse)
	}
	var created githubPullRequest
	if json.Unmarshal(response.body, &created) != nil {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	return successfulGitHubOutcome(command, created)
}

func (publisher *githubPublisher) findExisting(
	ctx context.Context,
	repositoryPath string,
	owner string,
	command v1.SourcePublicationCommand,
) (githubPullRequest, bool, *v1.SourcePublicationResultOutcome) {
	query := url.Values{
		"state": {"open"}, "head": {owner + ":" + command.HeadBranch}, "base": {command.BaseBranch},
	}
	response, err := publisher.request(ctx, http.MethodGet, repositoryPath+"/pulls", query, nil)
	if err != nil {
		outcome := retryable(v1.SourceControlProviderUnavailable, nil)
		return githubPullRequest{}, false, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitHubFailure(response, v1.SourceControlRepositoryNotFound)
		return githubPullRequest{}, false, &outcome
	}
	var requests []githubPullRequest
	if json.Unmarshal(response.body, &requests) != nil || len(requests) > 100 {
		outcome := permanent(v1.SourceControlInvalidProviderResponse)
		return githubPullRequest{}, false, &outcome
	}
	for _, request := range requests {
		if request.Head.SHA == command.HeadSHA && request.Base.Ref == command.BaseBranch && request.State == "open" {
			return request, true, nil
		}
	}
	if len(requests) != 0 {
		outcome := permanent(v1.SourceControlHeadMismatch)
		return githubPullRequest{}, false, &outcome
	}
	return githubPullRequest{}, false, nil
}

func (publisher *githubPublisher) request(
	ctx context.Context,
	method string,
	requestPath string,
	query url.Values,
	body []byte,
) (githubResponse, error) {
	target := *publisher.baseURL
	target.Path = strings.TrimSuffix(publisher.baseURL.Path, "/") + requestPath
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return githubResponse{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+publisher.token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := publisher.client.Do(request)
	if err != nil {
		return githubResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if err != nil || len(responseBody) > maxProviderResponseBytes {
		return githubResponse{}, fmt.Errorf("invalid provider response")
	}
	return githubResponse{status: response.StatusCode, header: response.Header.Clone(), body: responseBody}, nil
}

func githubRepository(value string) (string, string, bool) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !githubRepositoryComponentPattern.MatchString(parts[0]) ||
		!githubRepositoryComponentPattern.MatchString(parts[1]) || parts[0] == "." || parts[0] == ".." ||
		parts[1] == "." || parts[1] == ".." {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func successfulGitHubOutcome(
	command v1.SourcePublicationCommand,
	request githubPullRequest,
) v1.SourcePublicationResultOutcome {
	parsedURL, err := url.Parse(request.HTMLURL)
	if request.Number <= 0 || request.State != "open" || request.Head.SHA != command.HeadSHA ||
		request.Base.Ref != command.BaseBranch || err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" ||
		parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
	return v1.SourcePublicationResultOutcome{
		Kind: v1.SourcePublicationSucceeded,
		Result: &v1.SourcePublicationResult{
			SchemaVersion: command.SchemaVersion, Provider: command.Provider,
			InstallationID: command.InstallationID, RepositoryID: command.RepositoryID,
			RepositoryExternalID: command.RepositoryExternalID, BaseBranch: command.BaseBranch,
			HeadBranch: command.HeadBranch, HeadSHA: command.HeadSHA, IdempotencyKey: command.IdempotencyKey,
			ExternalRequestID: strconv.FormatInt(request.Number, 10), URL: request.HTMLURL, State: "open",
		},
	}
}

func classifyGitHubFailure(
	response githubResponse,
	notFoundCode v1.SourceControlErrorCode,
) v1.SourcePublicationResultOutcome {
	if response.status == http.StatusRequestTimeout || response.status >= 500 && response.status <= 599 {
		return retryable(v1.SourceControlProviderUnavailable, githubRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case http.StatusUnauthorized:
		return permanent(v1.SourceControlAuthenticationFailed)
	case http.StatusForbidden:
		if response.header.Get("X-RateLimit-Remaining") == "0" || response.header.Get("Retry-After") != "" {
			return retryable(v1.SourceControlRateLimited, githubRetryAfter(response.header, time.Now()))
		}
		return permanent(v1.SourceControlPermissionDenied)
	case http.StatusNotFound:
		return permanent(notFoundCode)
	case http.StatusTooManyRequests:
		return retryable(v1.SourceControlRateLimited, githubRetryAfter(response.header, time.Now()))
	default:
		return permanent(v1.SourceControlInvalidProviderResponse)
	}
}

func githubRetryAfter(header http.Header, now time.Time) *uint64 {
	value := header.Get("Retry-After")
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		milliseconds := seconds * 1000
		if milliseconds <= 24*60*60*1000 {
			return &milliseconds
		}
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		delay := retryAt.Sub(now)
		if delay > 24*time.Hour {
			return nil
		}
		// #nosec G115 -- the positive delay is bounded to 24 hours above.
		milliseconds := uint64(delay.Milliseconds())
		return &milliseconds
	}
	return nil
}
