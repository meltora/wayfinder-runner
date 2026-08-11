package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

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
	var env v1.Envelope
	if err := json.Unmarshal(record.Payload, &env); err != nil {
		return nil, fmt.Errorf("check evidence is malformed: %w", err)
	}
	if env.Version != v1.ProtocolVersion || env.Type != v1.TypeEvent || env.ID != eventID {
		return nil, fmt.Errorf("check evidence envelope does not match the derived event identity")
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("check evidence payload is malformed")
	}
	if payload["kind"] != "test_results" || payload["phase"] != "iterative" || payload["source"] != "runner" {
		return nil, fmt.Errorf("check evidence is not an iterative runner result")
	}
	if payload["job_id"] != jr.job.JobID || payload["attempt_id"] != jr.job.AttemptID ||
		payload["check_id"] != checkID || payload["suite"] != checkID {
		return nil, fmt.Errorf("check evidence identity does not match this attempt and check")
	}
	token, ok := payload["fencing_token"].(float64)
	if !ok || token != float64(jr.job.FencingToken) {
		return nil, fmt.Errorf("check evidence fencing token does not match this attempt")
	}
	resultName, ok := payload["result"].(string)
	if !ok || (resultName != "passed" && resultName != "failed" && resultName != "error") {
		return nil, fmt.Errorf("check evidence result is malformed")
	}
	repositoryID, ok := payload["repository_id"].(string)
	if !ok || repositoryID == "" {
		return nil, fmt.Errorf("check evidence repository is malformed")
	}
	duration, ok := payload["duration_ms"].(float64)
	if !ok || duration < 0 || duration != math.Trunc(duration) {
		return nil, fmt.Errorf("check evidence duration is malformed")
	}
	commitSHA, ok := payload["commit_sha"].(string)
	if !ok {
		return nil, fmt.Errorf("check evidence commit is malformed")
	}
	dirty, ok := payload["worktree_dirty"].(bool)
	if !ok {
		return nil, fmt.Errorf("check evidence worktree state is malformed")
	}
	result := map[string]any{
		"check_id":       checkID,
		"repository_id":  repositoryID,
		"result":         resultName,
		"duration_ms":    int64(duration),
		"commit_sha":     commitSHA,
		"worktree_dirty": dirty,
	}
	if resultName != "passed" {
		failures, ok := payload["failures"].([]any)
		if !ok || len(failures) > 1 {
			return nil, fmt.Errorf("check evidence failures are malformed")
		}
		output := ""
		if len(failures) == 1 {
			output, ok = failures[0].(string)
			if !ok {
				return nil, fmt.Errorf("check evidence failure output is malformed")
			}
		}
		truncated, ok := payload["truncated"].(bool)
		if !ok {
			return nil, fmt.Errorf("check evidence truncation flag is malformed")
		}
		result["output"] = output
		result["truncated"] = truncated
	}
	if class, present := payload["error_classification"]; present {
		classStr, ok := class.(string)
		if !ok || classStr == "" {
			return nil, fmt.Errorf("check evidence error classification is malformed")
		}
		result["error_classification"] = classStr
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
