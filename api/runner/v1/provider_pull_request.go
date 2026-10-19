package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	ProviderPullRequestTransportVersion = "provider-pull-request-v1"
	EventProviderPullRequestResult      = "provider_pull_request_result"
	ProviderPullRequestOperationObserve = "observe"
	ProviderPullRequestOperationMerge   = "merge"
	providerPullRequestTimeLayout       = "2006-01-02T15:04:05Z"
)

type ProviderPullRequestAuthority struct {
	AuthorityVersion     string    `json:"authority_version"`
	OperationID          string    `json:"operation_id"`
	Operation            string    `json:"operation"`
	OrganizationID       string    `json:"organization_id"`
	ProjectID            string    `json:"project_id"`
	WorkflowRunID        string    `json:"workflow_run_id"`
	RepositoryID         string    `json:"repository_id"`
	Provider             string    `json:"provider"`
	InstallationID       string    `json:"installation_id"`
	ProviderInstanceKey  string    `json:"provider_instance_key"`
	ProviderRepositoryID string    `json:"provider_repository_id"`
	PullRequestNumber    string    `json:"pull_request_number"`
	ExpectedHeadSHA      string    `json:"expected_head_sha"`
	MergeMethod          *string   `json:"merge_method"`
	LeaseID              string    `json:"lease_id"`
	FencingToken         uint64    `json:"fencing_token"`
	LeaseExpiresAt       time.Time `json:"lease_expires_at"`
	CommandDigest        string    `json:"command_digest"`
}

type ProviderPullRequestOutcomeKind string
type ProviderPullRequestErrorCode string

const (
	ProviderPullRequestOutcomeObserved       ProviderPullRequestOutcomeKind = "observed"
	ProviderPullRequestOutcomeMerged         ProviderPullRequestOutcomeKind = "merged"
	ProviderPullRequestOutcomeFailed         ProviderPullRequestOutcomeKind = "failed"
	ProviderPullRequestProviderUnavailable   ProviderPullRequestErrorCode   = "provider_unavailable"
	ProviderPullRequestRateLimited           ProviderPullRequestErrorCode   = "rate_limited"
	ProviderPullRequestPermissionDenied      ProviderPullRequestErrorCode   = "permission_denied"
	ProviderPullRequestInstallationInactive  ProviderPullRequestErrorCode   = "installation_inactive"
	ProviderPullRequestRepositoryMismatch    ProviderPullRequestErrorCode   = "repository_mismatch"
	ProviderPullRequestNotFound              ProviderPullRequestErrorCode   = "pull_request_not_found"
	ProviderPullRequestNotOpen               ProviderPullRequestErrorCode   = "pull_request_not_open"
	ProviderPullRequestHeadChanged           ProviderPullRequestErrorCode   = "head_changed"
	ProviderPullRequestNotMergeable          ProviderPullRequestErrorCode   = "not_mergeable"
	ProviderPullRequestMergeMethodNotAllowed ProviderPullRequestErrorCode   = "merge_method_not_allowed"
	ProviderPullRequestDeadlineExceeded      ProviderPullRequestErrorCode   = "deadline_exceeded"
)

type ProviderPullRequestResultOutcome struct {
	Kind                ProviderPullRequestOutcomeKind `json:"kind"`
	State               string                         `json:"state,omitempty"`
	HeadSHA             string                         `json:"head_sha,omitempty"`
	MergeCommitSHA      *string                        `json:"merge_commit_sha,omitempty"`
	Mergeable           *bool                          `json:"mergeable,omitempty"`
	AllowedMergeMethods []string                       `json:"allowed_merge_methods,omitempty"`
	Code                ProviderPullRequestErrorCode   `json:"code,omitempty"`
}

func (v ProviderPullRequestResultOutcome) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case ProviderPullRequestOutcomeObserved:
		return json.Marshal(struct {
			Kind                ProviderPullRequestOutcomeKind `json:"kind"`
			State               string                         `json:"state"`
			HeadSHA             string                         `json:"head_sha"`
			MergeCommitSHA      *string                        `json:"merge_commit_sha"`
			Mergeable           *bool                          `json:"mergeable"`
			AllowedMergeMethods []string                       `json:"allowed_merge_methods"`
		}{v.Kind, v.State, v.HeadSHA, v.MergeCommitSHA, v.Mergeable, v.AllowedMergeMethods})
	case ProviderPullRequestOutcomeMerged:
		sha := ""
		if v.MergeCommitSHA != nil {
			sha = *v.MergeCommitSHA
		}
		return json.Marshal(struct {
			Kind           ProviderPullRequestOutcomeKind `json:"kind"`
			MergeCommitSHA string                         `json:"merge_commit_sha"`
		}{v.Kind, sha})
	default:
		return json.Marshal(struct {
			Kind ProviderPullRequestOutcomeKind `json:"kind"`
			Code ProviderPullRequestErrorCode   `json:"code"`
		}{v.Kind, v.Code})
	}
}

type ProviderPullRequestResultEvent struct {
	Kind string `json:"kind"`
	ProviderPullRequestAuthority
	CommandMessageID string                           `json:"command_message_id"`
	ObservedAt       time.Time                        `json:"observed_at"`
	Outcome          ProviderPullRequestResultOutcome `json:"outcome"`
}

func ProviderPullRequestCommandDigest(a ProviderPullRequestAuthority) string {
	var mergeMethod any
	if a.MergeMethod != nil {
		mergeMethod = *a.MergeMethod
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode([]any{
		a.AuthorityVersion, a.OperationID, a.Operation, a.OrganizationID, a.ProjectID, a.WorkflowRunID,
		a.RepositoryID, a.Provider, a.InstallationID, a.ProviderInstanceKey, a.ProviderRepositoryID,
		a.PullRequestNumber, a.ExpectedHeadSHA, mergeMethod, a.LeaseID, a.FencingToken,
		a.LeaseExpiresAt.UTC().Format(providerPullRequestTimeLayout),
	})
	digest := sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	return hex.EncodeToString(digest[:])
}

func ProviderPullRequestCommandMessageID(operationID string) string {
	return "provider-pull-request-command-" + operationID
}

func ProviderPullRequestLeaseID(operationID string) string {
	return "provider-pull-request-lease-" + operationID
}

func ProviderPullRequestResultEventID(operationID string) string {
	digest := sha256.Sum256([]byte(operationID))
	return "provider-pull-request-result-" + hex.EncodeToString(digest[:])
}

func ProviderPullRequestTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Second)
}
