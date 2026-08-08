package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/hookspool"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

func (jr *jobRunner) handleToolCall(ctx context.Context, tool string, args json.RawMessage) (any, error) {
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return nil, fmt.Errorf("lease validation failed: %w", err)
	}
	if err := mcpbridge.ValidateSocketTool(tool); err != nil {
		return nil, err
	}
	if !toolAllowedForJob(jr.job.Kind, tool) {
		return nil, fmt.Errorf("tool %q is not allowed for %s jobs", tool, jr.job.Kind)
	}
	var raw map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	} else {
		raw = map[string]any{}
	}
	if err := validateToolArguments(raw, 0); err != nil {
		return nil, err
	}
	key := str(raw, "idempotency_key")
	if key == "" {
		if tool != "platform_get_job_context" {
			return nil, fmt.Errorf("idempotency_key is required")
		}
		return jr.executeToolCall(ctx, tool, raw)
	}
	if err := validation.ID("idempotency_key", key); err != nil {
		return nil, err
	}
	scope := tool + ":" + key
	jr.idempotencyMu.Lock()
	if existing, ok := jr.seenIdempotency[scope]; ok {
		jr.idempotencyMu.Unlock()
		select {
		case <-existing.done:
			if existing.err != nil {
				return nil, existing.err
			}
			return decodeToolResult(existing.result)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if len(jr.seenIdempotency) >= 10_000 {
		jr.idempotencyMu.Unlock()
		return nil, fmt.Errorf("idempotency-key limit reached for this attempt")
	}
	memo := &toolCallMemo{done: make(chan struct{})}
	jr.seenIdempotency[scope] = memo
	jr.idempotencyMu.Unlock()
	state, storedResult, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, tool, key)
	if err != nil {
		jr.finishToolMemo(scope, memo, nil, err)
		return nil, fmt.Errorf("record tool call: %w", err)
	}
	if state == journal.ToolCallCompleted {
		result, decodeErr := decodeToolResult(storedResult)
		jr.finishToolMemo(scope, memo, storedResult, decodeErr)
		return result, decodeErr
	}
	if state == journal.ToolCallPending {
		err := fmt.Errorf("a previous execution of this tool call is pending reconciliation")
		jr.finishToolMemo(scope, memo, nil, err)
		return nil, err
	}

	result, callErr := jr.executeToolCall(ctx, tool, raw)
	var encoded []byte
	if callErr == nil {
		var marshalErr error
		encoded, marshalErr = json.Marshal(result)
		if marshalErr != nil {
			callErr = fmt.Errorf("encode tool result: %w", marshalErr)
		} else if completeErr := jr.d.journal.CompleteToolCall(ctx, jr.job.AttemptID, tool, key, encoded); completeErr != nil {
			callErr = fmt.Errorf("commit tool call: %w", completeErr)
		}
	} else if abandonErr := jr.d.journal.AbandonToolCall(context.Background(), jr.job.AttemptID, tool, key); abandonErr != nil {
		jr.logger.Error("abandon failed tool call", "tool", tool, "err", abandonErr)
	}
	jr.finishToolMemo(scope, memo, encoded, callErr)
	return result, callErr
}

func (jr *jobRunner) finishToolMemo(scope string, memo *toolCallMemo, result []byte, err error) {
	jr.idempotencyMu.Lock()
	memo.result = append([]byte(nil), result...)
	memo.err = err
	if err != nil {
		delete(jr.seenIdempotency, scope)
	}
	close(memo.done)
	jr.idempotencyMu.Unlock()
}

func decodeToolResult(encoded []byte) (any, error) {
	if len(encoded) == 0 {
		return nil, fmt.Errorf("stored tool result is empty")
	}
	var result any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("decode stored tool result: %w", err)
	}
	return result, nil
}

func toolAllowedForJob(kind v1.JobKind, tool string) bool {
	if tool == mcpbridge.InternalHookEventTool {
		return true
	}
	if kind == v1.JobKindBuild {
		return true
	}
	if kind != v1.JobKindPlan {
		return false
	}
	switch tool {
	case "platform_get_job_context", "platform_publish_plan", "platform_publish_plan_revision",
		"platform_report_progress", "platform_checkpoint", "platform_report_failure",
		"platform_report_blocker", "platform_request_human_input", "platform_publish_decision":
		return true
	default:
		return false
	}
}

func capabilitiesForJob(kind v1.JobKind) []string {
	capabilities := make([]string, 0, len(mcpbridge.Tools))
	for _, tool := range mcpbridge.Tools {
		if toolAllowedForJob(kind, tool.Name) {
			capabilities = append(capabilities, tool.Name)
		}
	}
	return capabilities
}

func (jr *jobRunner) executeToolCall(ctx context.Context, tool string, raw map[string]any) (any, error) {
	switch tool {
	case mcpbridge.InternalHookEventTool:
		eventType := str(raw, "event_type")
		if err := mcpbridge.ValidateHookEventType(eventType); err != nil {
			return nil, err
		}
		for field := range raw {
			if field != "event_type" && field != "idempotency_key" {
				return nil, fmt.Errorf("hook telemetry field %q is not allowed", field)
			}
		}
		spool, err := hookspool.NewSpool(filepath.Join(jr.d.cfg.StateDir, "hook-spool"))
		if err != nil {
			return nil, err
		}
		if err := spool.Append(hookspool.Event{
			Type: eventType, Timestamp: time.Now().UTC(), JobID: jr.job.JobID,
			AttemptID: jr.job.AttemptID,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"recorded": true}, nil

	case "platform_get_job_context":
		return map[string]any{
			"job_id":       jr.job.JobID,
			"attempt_id":   jr.job.AttemptID,
			"kind":         jr.job.Kind,
			"ticket":       jr.job.Ticket,
			"repositories": jr.job.Repositories,
			"capabilities": capabilitiesForJob(jr.job.Kind),
		}, nil

	case "platform_publish_plan":
		if err := requireToolText(raw, "summary", 16<<10); err != nil {
			return nil, err
		}
		if err := requireToolText(raw, "approach", 128<<10); err != nil {
			return nil, err
		}
		digest := digestOf(raw, "summary", "approach")
		rev, err := jr.d.plans.NextRevision(jr.job.JobID, 0, digest)
		if err != nil {
			return nil, fmt.Errorf("plan already published; use platform_publish_plan_revision with base_revision %d", 1)
		}
		plan := v1.PlanRevision{
			JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, FencingToken: jr.job.FencingToken,
			Revision:      rev,
			Summary:       str(raw, "summary"),
			Approach:      str(raw, "approach"),
			TestStrategy:  str(raw, "test_strategy"),
			Requirements:  strSlice(raw, "requirements"),
			ExpectedFiles: strSlice(raw, "expected_files"),
			Risks:         strSlice(raw, "risks"),
			Assumptions:   strSlice(raw, "assumptions"),
			PublishedAt:   time.Now().UTC(),
		}
		if err := jr.d.sendEvent(ctx, map[string]any{"kind": "plan_published", "plan": plan}); err != nil {
			return nil, err
		}
		return map[string]any{"revision": plan.Revision, "published": true}, nil

	case "platform_report_progress":
		if err := requireToolText(raw, "milestone", 1024); err != nil {
			return nil, err
		}
		jr.publishProgress(ctx, str(raw, "milestone"), str(raw, "current_task"), str(raw, "next_action"))
		return map[string]any{"ok": true}, nil

	case "platform_checkpoint":
		cp := collector.NewCheckpoint(jr.job, str(raw, "plan_step"), str(raw, "branch_state"), str(raw, "next_action"))
		cp.ProviderSessionID = jr.currentSessionID()
		if err := jr.d.collectors.SaveCheckpoint(cp); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "platform_report_failure":
		if err := requireToolText(raw, "failure_type", 1024); err != nil {
			return nil, err
		}
		if err := requireToolText(raw, "message", 64<<10); err != nil {
			return nil, err
		}
		if err := jr.d.sendEvent(ctx, map[string]any{
			"kind":          "job_failure",
			"job_id":        jr.job.JobID,
			"attempt_id":    jr.job.AttemptID,
			"fencing_token": jr.job.FencingToken,
			"failure_type":  str(raw, "failure_type"),
			"message":       str(raw, "message"),
			"retryable":     raw["retryable"],
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "platform_publish_plan_revision":
		base, _ := raw["base_revision"].(float64)
		if base < 1 || base != math.Trunc(base) {
			return nil, fmt.Errorf("base_revision must be a positive integer")
		}
		for _, field := range []string{"change_summary", "summary", "approach"} {
			if err := requireToolText(raw, field, 128<<10); err != nil {
				return nil, err
			}
		}
		digest := digestOf(raw, "summary", "approach", "change_summary")
		rev, err := jr.d.plans.NextRevision(jr.job.JobID, int(base), digest)
		if err != nil {
			return nil, err
		}
		plan := v1.PlanRevision{
			JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, FencingToken: jr.job.FencingToken,
			BaseRevision: int(base), Revision: rev,
			Summary: str(raw, "summary"), Approach: str(raw, "approach"),
			TestStrategy: str(raw, "test_strategy"),
			Requirements: strSlice(raw, "requirements"), ExpectedFiles: strSlice(raw, "expected_files"),
			Risks: strSlice(raw, "risks"), Assumptions: strSlice(raw, "assumptions"),
			PublishedAt: time.Now().UTC(),
		}
		if err := jr.d.sendEvent(ctx, map[string]any{
			"kind": "plan_published", "change_summary": str(raw, "change_summary"), "plan": plan,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"revision": rev, "published": true}, nil

	case "platform_report_blocker":
		if err := requireToolText(raw, "blocker_type", 1024); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, jr.sendJobEvent(ctx, "blocker", map[string]any{
			"blocker_type": str(raw, "blocker_type"), "impact": str(raw, "impact"),
			"attempted_resolution": str(raw, "attempted_resolution"),
			"recommended_action":   str(raw, "recommended_action"),
			"can_continue":         raw["can_continue"],
		})

	case "platform_request_human_input":
		if err := requireToolText(raw, "question", 16<<10); err != nil {
			return nil, err
		}
		questionID := str(raw, "idempotency_key")
		if questionID == "" {
			questionID = "q-" + newEventID()
		}
		timeout := defaultDecisionTimeout
		if ts, ok := raw["timeout_seconds"].(float64); ok && ts > 0 {
			timeout = time.Duration(ts) * time.Second
			if timeout > maxDecisionTimeout {
				timeout = maxDecisionTimeout
			}
		}
		ch := make(chan v1.CommandPayload, 1)
		jr.decisionMu.Lock()
		jr.decisions[questionID] = ch
		jr.decisionMu.Unlock()
		defer func() {
			jr.decisionMu.Lock()
			delete(jr.decisions, questionID)
			jr.decisionMu.Unlock()
		}()

		if err := jr.sendJobEvent(ctx, "decision_request", map[string]any{
			"question_id": questionID,
			"question":    str(raw, "question"), "options": strSlice(raw, "options"),
			"recommendation":       str(raw, "recommendation"),
			"consequence_of_delay": str(raw, "consequence_of_delay"),
			"blocked_operation":    str(raw, "blocked_operation"),
		}); err != nil {
			return nil, err
		}
		select {
		case answer := <-ch:
			return map[string]any{
				"status": "answered", "question_id": questionID,
				"decision": answer.Decision, "decided_by": answer.DecidedBy,
			}, nil
		case <-jr.ctx.Done():
			return nil, fmt.Errorf("job cancelled while waiting for human input")
		case <-time.After(timeout):
			_ = jr.sendJobEvent(context.Background(), "decision_timeout", map[string]any{
				"question_id":     questionID,
				"question":        str(raw, "question"),
				"timeout_seconds": int(timeout.Seconds()),
			})
			return map[string]any{"status": "timeout", "question_id": questionID}, nil
		}

	case "platform_publish_decision":
		if err := requireToolText(raw, "decision", 16<<10); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, jr.sendJobEvent(ctx, "decision", map[string]any{
			"decision": str(raw, "decision"), "rationale": str(raw, "rationale"),
			"evidence": strSlice(raw, "evidence"),
		})

	case "platform_publish_test_results":
		if err := requireToolText(raw, "suite", 1024); err != nil {
			return nil, err
		}
		result := str(raw, "result")
		if result != "passed" && result != "failed" && result != "error" {
			return nil, fmt.Errorf("result must be passed, failed, or error")
		}
		if duration, ok := raw["duration_ms"].(float64); ok && (duration < 0 || duration != math.Trunc(duration) || duration > 86_400_000) {
			return nil, fmt.Errorf("duration_ms is invalid")
		}
		return map[string]any{"ok": true}, jr.sendJobEvent(ctx, "test_results", map[string]any{
			"suite": str(raw, "suite"), "command": str(raw, "command"), "result": result,
			"duration_ms": raw["duration_ms"], "failures": strSlice(raw, "failures"),
			"commit_sha": str(raw, "commit_sha"),
		})

	case "platform_publish_manual_test_plan":
		return map[string]any{"ok": true}, jr.sendJobEvent(ctx, "manual_test_plan", map[string]any{
			"steps": strSlice(raw, "steps"), "prerequisites": strSlice(raw, "prerequisites"),
			"expected_results": strSlice(raw, "expected_results"), "cleanup": str(raw, "cleanup"),
		})

	case "platform_publish_preview":
		if err := validation.WebURL("preview url", str(raw, "url")); err != nil {
			return nil, err
		}
		health := str(raw, "health")
		if health != "healthy" && health != "degraded" && health != "down" {
			return nil, fmt.Errorf("preview health must be healthy, degraded, or down")
		}
		return map[string]any{"ok": true}, jr.sendJobEvent(ctx, "preview", map[string]any{
			"url": str(raw, "url"), "commit_sha": str(raw, "commit_sha"),
			"health": str(raw, "health"), "expires_at": str(raw, "expires_at"),
			"known_limitations": strSlice(raw, "known_limitations"),
		})

	case "platform_publish_artifact":
		return jr.handlePublishArtifact(ctx, raw)

	case "platform_mark_review_ready":
		if err := requireToolText(raw, "summary", 64<<10); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "status": "review_requested"}, jr.sendJobEvent(ctx, "review_ready", map[string]any{
			"summary": str(raw, "summary"), "known_limitations": strSlice(raw, "known_limitations"),
		})
	}
	return nil, fmt.Errorf("unknown tool %q", tool)
}

func requireToolText(raw map[string]any, field string, limit int) error {
	value, ok := raw[field].(string)
	if !ok || value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > limit || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s exceeds its size limit or contains NUL", field)
	}
	return nil
}

func validateToolArguments(value any, depth int) error {
	if depth > 5 {
		return fmt.Errorf("tool arguments exceed maximum nesting depth")
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 64 {
			return fmt.Errorf("tool arguments contain too many fields")
		}
		for key, child := range typed {
			if len(key) > 128 {
				return fmt.Errorf("tool argument field name is too long")
			}
			if err := validateToolArguments(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > 256 {
			return fmt.Errorf("tool argument array is too large")
		}
		for _, child := range typed {
			if err := validateToolArguments(child, depth+1); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > 64<<10 {
			return fmt.Errorf("tool argument string is too large")
		}
	}
	return nil
}

const (
	defaultDecisionTimeout = 4 * time.Hour
	maxDecisionTimeout     = 4 * time.Hour
)

func (jr *jobRunner) deliverDecision(cmd v1.CommandPayload) error {
	if err := validation.ID("question_id", cmd.QuestionID); err != nil {
		return fmt.Errorf("decision_response: %w", err)
	}
	if cmd.Decision == "" || len(cmd.Decision) > 16<<10 || len(cmd.DecidedBy) > 1024 ||
		strings.ContainsRune(cmd.Decision, '\x00') || strings.ContainsRune(cmd.DecidedBy, '\x00') {
		return fmt.Errorf("decision_response has invalid decision metadata")
	}
	jr.decisionMu.Lock()
	ch, ok := jr.decisions[cmd.QuestionID]
	jr.decisionMu.Unlock()
	if !ok {
		return fmt.Errorf("no pending question %s", cmd.QuestionID)
	}
	select {
	case ch <- cmd:
		return nil
	default:
		return fmt.Errorf("question %s already answered", cmd.QuestionID)
	}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func (jr *jobRunner) sendJobEvent(ctx context.Context, kind string, fields map[string]any) error {
	payload := make(map[string]any, len(fields)+4)
	for key, value := range fields {
		payload[key] = value
	}
	payload["kind"] = kind
	payload["job_id"] = jr.job.JobID
	payload["attempt_id"] = jr.job.AttemptID
	payload["fencing_token"] = jr.job.FencingToken
	return jr.d.sendEvent(ctx, payload)
}

func digestOf(raw map[string]any, keys ...string) string {
	h := sha256.New()
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "%s=%v;", k, raw[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (jr *jobRunner) handlePublishArtifact(ctx context.Context, raw map[string]any) (any, error) {
	name := str(raw, "name")
	relPath := str(raw, "path")
	contentType := str(raw, "content_type")
	if name == "" || relPath == "" {
		return nil, fmt.Errorf("name and path are required")
	}
	if contentType != "" {
		if len(contentType) > 255 {
			return nil, fmt.Errorf("content_type is too long")
		}
		if _, _, err := mime.ParseMediaType(contentType); err != nil {
			return nil, fmt.Errorf("invalid content_type: %w", err)
		}
	}
	f, err := safeio.OpenRegular(jr.workDir, relPath)
	if err != nil {
		return nil, fmt.Errorf("artifact source: %w", err)
	}
	defer func() { _ = f.Close() }()
	ref, storedPath, err := jr.d.collectors.SaveArtifact(jr.job.JobID, jr.job.AttemptID, jr.job.FencingToken, name, contentType, f)
	if err != nil {
		return nil, err
	}
	pending, err := jr.d.queueArtifactUpload(ctx, *ref, storedPath)
	if err != nil {
		_ = os.Remove(storedPath)
		return nil, fmt.Errorf("queue artifact upload: %w", err)
	}
	uploadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := jr.d.processArtifactUpload(uploadCtx, *pending); err != nil {
		jr.logger.Warn("artifact upload queued for retry", "record_id", pending.RecordID, "err", err)
		return map[string]any{
			"queued": true, "registered": false, "digest": ref.DigestSHA256,
			"size_bytes": ref.SizeBytes,
		}, nil
	}
	return map[string]any{
		"queued": false, "registered": true, "digest": ref.DigestSHA256,
		"size_bytes": ref.SizeBytes,
	}, nil
}

func strSlice(m map[string]any, key string) []string {
	items, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
