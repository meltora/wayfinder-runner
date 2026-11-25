package validation

import (
	"fmt"
	"regexp"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var previewDeployRequestIDPattern = regexp.MustCompile(`^preview-deploy-request-[0-9a-f]{64}$`)

func PreviewDeployRequestedEvent(e v1.PreviewDeployRequestedEvent) error {
	if e.SchemaVersion != v1.PreviewDeployRequestedSchemaVersion {
		return fmt.Errorf("preview deploy requested schema_version is unsupported")
	}
	for _, field := range []struct{ name, value string }{
		{"job_id", e.JobID},
		{"lease_id", e.LeaseID},
		{"request_id", e.RequestID},
	} {
		if err := ID(field.name, field.value); err != nil {
			return err
		}
	}
	if !previewDeployRequestIDPattern.MatchString(e.RequestID) {
		return fmt.Errorf("preview deploy requested request_id is invalid")
	}
	if e.FencingToken == 0 || e.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("preview deploy requested fencing_token is invalid")
	}
	if err := validatePreviewObservationCommits(e.CommitSet); err != nil {
		return err
	}
	for _, commit := range e.CommitSet {
		if len(commit.CommitSHA) != 40 {
			return fmt.Errorf("preview deploy requested commit_sha must be 40 lowercase hex characters")
		}
	}
	if err := SHA256Digest("commit_set_digest", e.CommitSetDigest); err != nil {
		return err
	}
	if e.CommitSetDigest != v1.PreviewLifecycleCommitSetDigest(e.CommitSet) {
		return fmt.Errorf("preview deploy requested commit_set_digest does not match commit_set")
	}
	requestedAt, err := time.Parse(time.RFC3339Nano, e.RequestedAt)
	if err != nil || v1.FormatPreviewJobDeployTime(requestedAt) != e.RequestedAt {
		return fmt.Errorf("preview deploy requested requested_at must be RFC3339 UTC with milliseconds")
	}
	return nil
}
