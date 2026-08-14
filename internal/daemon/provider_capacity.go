package daemon

import (
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/provider"
	"github.com/example/app-runner/internal/validation"
)

type capacityRecoveryLineage struct {
	BranchRef string
	CommitSHA string
}

func (jr *jobRunner) capacityRecoveryLineage(finalized map[string]any) *capacityRecoveryLineage {
	if jr.job.Kind != v1.JobKindBuild || len(jr.repositories) != 1 {
		return nil
	}
	pushed, _ := finalized["pushed"].(bool)
	branch, branchOK := finalized["branch"].(string)
	commitSHA, commitOK := finalized["head_sha"].(string)
	if !pushed || !branchOK || !commitOK || branch != jr.repositories[0].Branch {
		return nil
	}
	if validation.GitRef("branch_ref", branch) != nil || validation.GitCommitSHA("commit_sha", commitSHA) != nil {
		return nil
	}
	return &capacityRecoveryLineage{BranchRef: branch, CommitSHA: commitSHA}
}

func (jr *jobRunner) providerCapacityEvent(
	failure provider.CapacityFailure,
	lineage *capacityRecoveryLineage,
	checkpointRef string,
) v1.ProviderCapacityExhaustedEvent {
	event := v1.ProviderCapacityExhaustedEvent{
		Kind:           "provider_capacity_exhausted",
		ManifestKind:   jr.job.Kind,
		LeaseID:        jr.job.LeaseID,
		JobID:          jr.job.JobID,
		AttemptID:      jr.job.AttemptID,
		ManifestID:     jr.job.ManifestID,
		ManifestDigest: jr.job.ManifestDigest,
		FencingToken:   jr.job.FencingToken,
		ObservedAt:     time.Now().UTC(),
		Adapter:        jr.d.cfg.Provider,
		Provider:       provider.ProviderIdentity(jr.d.cfg.Provider, jr.d.cfg.AgentModel),
		Model:          jr.d.cfg.AgentModel,
		Reason:         string(failure.Reason),
		ResumePolicy:   jr.job.ProviderCapacityResumePolicy,
		Message:        truncateUTF8Bytes(jr.currentProviderError(), 512),
	}
	if event.ResumePolicy == "" {
		event.ResumePolicy = v1.CapacityResumeManual
	}
	if !failure.RetryAt.IsZero() {
		retryAt := failure.RetryAt.UTC()
		event.RetryAt = &retryAt
	}
	if sessionID := jr.currentSessionID(); sessionID != "" {
		event.ProviderSessionID = sessionID
	}
	if lineage != nil {
		event.BranchRef = lineage.BranchRef
		event.CommitSHA = lineage.CommitSHA
		event.CheckpointRef = checkpointRef
	}
	return event
}

func truncateUTF8Bytes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "")
	if len(value) <= maximum {
		return value
	}
	cutoff := maximum
	for cutoff > 0 && !utf8.ValidString(value[:cutoff]) {
		cutoff--
	}
	return value[:cutoff]
}

func (jr *jobRunner) persistCapacityCheckpoint(lineage *capacityRecoveryLineage) string {
	if lineage == nil {
		return ""
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return ""
	}
	checkpoint, err := jr.d.collectors.CheckpointForAttempt(
		jr.job.JobID,
		jr.job.AttemptID,
		jr.job.FencingToken,
	)
	sessionID := jr.currentSessionID()
	if err != nil {
		if sessionID == "" {
			return ""
		}
		created := collector.NewCheckpoint(
			jr.job,
			"provider capacity wait",
			lineage.BranchRef+"@"+lineage.CommitSHA,
			"resume provider session",
		)
		checkpoint = &created
	}
	checkpoint.BranchState = lineage.BranchRef + "@" + lineage.CommitSHA
	if sessionID != "" {
		checkpoint.ProviderSessionID = sessionID
	}
	checkpoint.CreatedAt = time.Now().UTC()
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return ""
	}
	if err := jr.d.collectors.SaveCheckpoint(*checkpoint); err != nil {
		jr.logger.Warn("persist provider capacity checkpoint", "err", err)
		return ""
	}
	return jr.job.AttemptID
}
