package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

// runCheckRecoveryEnvelope decodes durable check evidence without a float64
// round trip: the payload stays raw so integer fields keep full precision.
type runCheckRecoveryEnvelope struct {
	Version   int             `json:"version"`
	Type      v1.MessageType  `json:"type"`
	ID        string          `json:"id"`
	SentAt    time.Time       `json:"sent_at"`
	RunnerID  string          `json:"runner_id,omitempty"`
	InReplyTo string          `json:"in_reply_to,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// runCheckRecoveryPayload keeps fencing_token and duration_ms as exact
// integers. Pointer fields distinguish a missing key from a zero value so the
// strict presence validation of the durable record is preserved.
type runCheckRecoveryPayload struct {
	Kind          string          `json:"kind"`
	Phase         string          `json:"phase"`
	Source        string          `json:"source"`
	JobID         string          `json:"job_id"`
	AttemptID     string          `json:"attempt_id"`
	CheckID       string          `json:"check_id"`
	Suite         string          `json:"suite"`
	FencingToken  *uint64         `json:"fencing_token"`
	Result        string          `json:"result"`
	RepositoryID  string          `json:"repository_id"`
	DurationMS    *int64          `json:"duration_ms"`
	CommitSHA     *string         `json:"commit_sha"`
	WorktreeDirty *bool           `json:"worktree_dirty"`
	Failures      *[]string       `json:"failures"`
	Truncated     *bool           `json:"truncated"`
	ErrorClass    json.RawMessage `json:"error_classification"`
}

func (jr *jobRunner) recoverRunCheckResult(ctx context.Context, checkID, key string) (map[string]any, error) {
	eventID := runCheckEventID(jr.job.AttemptID, checkID, key)
	record, ok, err := jr.d.journal.LoadRecord(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("load check evidence: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("check evidence for this idempotency key is not durably recorded")
	}
	if record.Kind != journal.KindEvent {
		return nil, fmt.Errorf("check evidence record has unexpected kind %q", record.Kind)
	}
	var env runCheckRecoveryEnvelope
	if err := json.Unmarshal(record.Payload, &env); err != nil {
		return nil, fmt.Errorf("check evidence is malformed: %w", err)
	}
	if env.Version != v1.ProtocolVersion || env.Type != v1.TypeEvent || env.ID != eventID {
		return nil, fmt.Errorf("check evidence envelope does not match the derived event identity")
	}
	if len(env.Payload) == 0 {
		return nil, fmt.Errorf("check evidence payload is malformed")
	}
	var payload runCheckRecoveryPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return nil, fmt.Errorf("check evidence payload is malformed: %w", err)
	}
	if payload.Kind != "test_results" || payload.Phase != "iterative" || payload.Source != "runner" {
		return nil, fmt.Errorf("check evidence is not an iterative runner result")
	}
	if payload.JobID != jr.job.JobID || payload.AttemptID != jr.job.AttemptID ||
		payload.CheckID != checkID || payload.Suite != checkID {
		return nil, fmt.Errorf("check evidence identity does not match this attempt and check")
	}
	if payload.FencingToken == nil || *payload.FencingToken != jr.job.FencingToken {
		return nil, fmt.Errorf("check evidence fencing token does not match this attempt")
	}
	resultName := payload.Result
	if resultName != "passed" && resultName != "failed" && resultName != "error" {
		return nil, fmt.Errorf("check evidence result is malformed")
	}
	if payload.RepositoryID == "" {
		return nil, fmt.Errorf("check evidence repository is malformed")
	}
	if payload.DurationMS == nil || *payload.DurationMS < 0 {
		return nil, fmt.Errorf("check evidence duration is malformed")
	}
	if payload.CommitSHA == nil {
		return nil, fmt.Errorf("check evidence commit is malformed")
	}
	if payload.WorktreeDirty == nil {
		return nil, fmt.Errorf("check evidence worktree state is malformed")
	}
	result := map[string]any{
		"check_id":       checkID,
		"repository_id":  payload.RepositoryID,
		"result":         resultName,
		"duration_ms":    *payload.DurationMS,
		"commit_sha":     *payload.CommitSHA,
		"worktree_dirty": *payload.WorktreeDirty,
	}
	if resultName != "passed" {
		if payload.Failures == nil || len(*payload.Failures) > 1 {
			return nil, fmt.Errorf("check evidence failures are malformed")
		}
		output := ""
		if len(*payload.Failures) == 1 {
			output = (*payload.Failures)[0]
		}
		if payload.Truncated == nil {
			return nil, fmt.Errorf("check evidence truncation flag is malformed")
		}
		result["output"] = output
		result["truncated"] = *payload.Truncated
	}
	if len(payload.ErrorClass) > 0 {
		var class string
		if err := json.Unmarshal(payload.ErrorClass, &class); err != nil || class == "" {
			return nil, fmt.Errorf("check evidence error classification is malformed")
		}
		result["error_classification"] = class
	}
	return result, nil
}

func (jr *jobRunner) reconcilePendingRunCheck(ctx context.Context, scopeTool, checkID, key string) (any, []byte, error) {
	recovered, err := jr.recoverRunCheckResult(ctx, checkID, key)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(recovered)
	if err != nil {
		return nil, nil, fmt.Errorf("encode recovered check result: %w", err)
	}
	if err := jr.d.journal.CompleteToolCall(ctx, jr.job.AttemptID, scopeTool, key, encoded); err != nil {
		state, stored, beginErr := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, key)
		if beginErr == nil && state == journal.ToolCallCompleted {
			decoded, decodeErr := decodeToolResult(stored)
			return decoded, stored, decodeErr
		}
		return nil, nil, fmt.Errorf("commit recovered check result: %w", err)
	}
	return recovered, encoded, nil
}
