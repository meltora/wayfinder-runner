package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"
)

const (
	EventPreviewDeployRequested         = "preview_deploy_requested"
	PreviewDeployRequestedSchemaVersion = 1
	PreviewJobDeployMaxRequests         = 6
	PreviewJobDeployCommandTimeout      = 2 * time.Minute
	PreviewJobDeployInternalHost        = "our-preview"
	PreviewJobDeployInternalURL         = "http://" + PreviewJobDeployInternalHost + ":8080/"
	PreviewJobDeployTool                = "platform_deploy_preview"
	PreviewJobDeployMaxCommits          = 16
	PreviewJobDeployTimeLayout          = "2006-01-02T15:04:05.000Z07:00"
)

const (
	PreviewJobDeployStatusDeployed = "deployed"
	PreviewJobDeployStatusRefused  = "refused"
	PreviewJobDeployStatusFailed   = "failed"
	PreviewJobDeployStatusTimeout  = "timeout"
)

const (
	PreviewJobDeployRefusalRejected             = "rejected"
	PreviewJobDeployRefusalTimestampOutOfWindow = "timestamp_out_of_window"
)

var previewJobDeployPassThroughRefusalCodes = []string{
	"not_build_job",
	"no_approved_blueprint",
	"commit_set_mismatch",
	"request_in_flight",
	"deploy_limit_reached",
	"runner_capability_missing",
	"emergency_stop_active",
	"lease_not_found",
	"lease_not_active",
	"lease_expired",
	"delivery_conflict",
	"decision_expired",
}

type PreviewDeployRequestedEvent struct {
	SchemaVersion   int                        `json:"schema_version"`
	JobID           string                     `json:"job_id"`
	LeaseID         string                     `json:"lease_id"`
	FencingToken    uint64                     `json:"fencing_token"`
	RequestID       string                     `json:"request_id"`
	CommitSet       []PreviewObservationCommit `json:"commit_set"`
	CommitSetDigest string                     `json:"commit_set_digest"`
	RequestedAt     string                     `json:"requested_at"`
}

func previewJobDeployHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func PreviewDeployRequestID(jobID, leaseID string, sequence uint64) string {
	return "preview-deploy-request-" + previewJobDeployHash(jobID+"\x00"+leaseID+"\x00"+strconv.FormatUint(sequence, 10))
}

func PreviewDeployRequestedEventID(requestID string) string {
	return "preview-deploy-requested-" + previewJobDeployHash(requestID)
}

func PreviewJobDeployIdempotencyKey(requestID string) string {
	return "preview-deploy-" + previewJobDeployHash("job-request\x00"+requestID)
}

func PreviewCommitSetFromMap(set map[string]string) ([]PreviewObservationCommit, error) {
	if len(set) < 1 || len(set) > PreviewJobDeployMaxCommits {
		return nil, fmt.Errorf("preview commit set must have 1-%d entries", PreviewJobDeployMaxCommits)
	}
	commits := make([]PreviewObservationCommit, 0, len(set))
	for repositoryID, commitSHA := range set {
		if repositoryID == "" {
			return nil, fmt.Errorf("preview commit set repository_id is required")
		}
		if !isPreviewJobDeployCommitSHA(commitSHA) {
			return nil, fmt.Errorf("preview commit set commit_sha for %q must be 40 lowercase hex characters", repositoryID)
		}
		commits = append(commits, PreviewObservationCommit{RepositoryID: repositoryID, CommitSHA: commitSHA})
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].RepositoryID < commits[j].RepositoryID })
	return commits, nil
}

func isPreviewJobDeployCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func PreviewJobDeployRefusalCode(code string) (string, bool) {
	if code == PreviewJobDeployRefusalTimestampOutOfWindow {
		return "", false
	}
	for _, passThrough := range previewJobDeployPassThroughRefusalCodes {
		if code == passThrough {
			return code, true
		}
	}
	return PreviewJobDeployRefusalRejected, true
}

func FormatPreviewJobDeployTime(t time.Time) string {
	return t.UTC().Format(PreviewJobDeployTimeLayout)
}
