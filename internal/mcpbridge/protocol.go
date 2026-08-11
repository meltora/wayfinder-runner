package mcpbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func decodeStrict(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("trailing JSON is not allowed")
	}
	return nil
}

const InternalHookEventTool = "platform_record_hook_event"

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	ErrParse     = -32700
	ErrInvalid   = -32600
	ErrNoMethod  = -32601
	ErrParams    = -32602
	ErrInternal  = -32603
	ErrForbidden = -32001
)

type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

var Tools = []ToolDef{
	{Name: "platform_get_job_context", Description: "Returns approved ticket context, plan status, repository roles, and execution boundaries for the current job.", InputSchema: objectSchema(map[string]any{})},
	{Name: "platform_publish_plan", Description: "Creates an immutable plan revision for human review.", InputSchema: objectSchema(map[string]any{
		"summary": stringProp("Plan summary"), "approach": stringProp("Technical approach"),
		"requirements": arrayProp("Requirements"), "expected_files": arrayProp("Expected file impact"),
		"risks": arrayProp("Risks"), "assumptions": arrayProp("Assumptions"),
		"test_strategy": stringProp("Test strategy"), "base_revision": numberProp("Base revision, 0 for first"),
		"idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_report_progress", Description: "Publishes a structured progress milestone.", InputSchema: objectSchema(map[string]any{
		"milestone": stringProp("Milestone"), "current_task": stringProp("Current task"),
		"next_action": stringProp("Next action"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_checkpoint", Description: "Publishes a safe resume point.", InputSchema: objectSchema(map[string]any{
		"plan_step": stringProp("Current plan step"), "branch_state": stringProp("Branch/commit state"),
		"next_action": stringProp("Next action"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_report_failure", Description: "Publishes a typed failure with recovery recommendation.", InputSchema: objectSchema(map[string]any{
		"failure_type": stringProp("Failure type"), "message": stringProp("Message"),
		"retryable": boolProp("Safe to retry"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_publish_plan_revision", Description: "Creates a new plan revision from an existing base revision. Rejected if the base is stale.", InputSchema: objectSchema(map[string]any{
		"base_revision": numberProp("Base revision to build on"), "change_summary": stringProp("What changed and why"),
		"summary": stringProp("Plan summary"), "approach": stringProp("Technical approach"),
		"requirements": arrayProp("Requirements"), "expected_files": arrayProp("Expected file impact"),
		"risks": arrayProp("Risks"), "assumptions": arrayProp("Assumptions"),
		"test_strategy": stringProp("Test strategy"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_report_blocker", Description: "Reports a typed blocker and whether work can safely continue elsewhere.", InputSchema: objectSchema(map[string]any{
		"blocker_type": stringProp("Blocker type"), "impact": stringProp("Impact"),
		"attempted_resolution": stringProp("What was tried"), "recommended_action": stringProp("Recommended human or system action"),
		"can_continue": boolProp("Can safely continue elsewhere"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_request_human_input", Description: "Creates a decision request for a human and blocks until the human answers, the timeout elapses, or the job is cancelled.", InputSchema: objectSchema(map[string]any{
		"question": stringProp("Decision needed"), "options": arrayProp("Options"),
		"recommendation": stringProp("Recommended option"), "consequence_of_delay": stringProp("Consequence of delay"),
		"blocked_operation": stringProp("Exact operation waiting"),
		"timeout_seconds":   numberProp("Max seconds to wait (default 14400, max 14400)"),
		"idempotency_key":   stringProp("Idempotency key (also used as the question id)"),
	})},
	{Name: "platform_publish_decision", Description: "Records an agent-observed implementation decision. Does not replace human approval.", InputSchema: objectSchema(map[string]any{
		"decision": stringProp("Decision made"), "rationale": stringProp("Rationale"),
		"evidence": arrayProp("Supporting evidence references"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_publish_test_results", Description: "Publishes test suite results with provenance.", InputSchema: objectSchema(map[string]any{
		"suite": stringProp("Suite identity"), "command": stringProp("Command run"),
		"result": stringProp("passed|failed|error"), "duration_ms": numberProp("Duration ms"),
		"failures": arrayProp("Failure summaries"), "commit_sha": stringProp("Commit set tested"),
		"idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_publish_manual_test_plan", Description: "Publishes manual validation steps for a human reviewer.", InputSchema: objectSchema(map[string]any{
		"steps": arrayProp("Ordered steps"), "prerequisites": arrayProp("Prerequisites"),
		"expected_results": arrayProp("Expected results"), "cleanup": stringProp("Cleanup instructions"),
		"idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_publish_preview", Description: "Publishes preview environment information bound to the commit set.", InputSchema: objectSchema(map[string]any{
		"url": stringProp("Preview URL"), "commit_sha": stringProp("Commit set"),
		"health": stringProp("healthy|degraded|down"), "expires_at": stringProp("Expiry ISO time"),
		"known_limitations": arrayProp("Known limitations"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_publish_artifact", Description: "Uploads a file from the job workspace as a registered artifact.", InputSchema: objectSchema(map[string]any{
		"name": stringProp("Artifact name"), "path": stringProp("Path inside the job workspace"),
		"content_type": stringProp("MIME type"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_run_check", Description: "Runs a registered required check on demand inside the job sandbox. Iterative results never satisfy final required checks.", InputSchema: runCheckSchema()},
	{Name: "platform_mark_review_ready", Description: "Requests transition to review readiness. The control plane independently verifies evidence.", InputSchema: objectSchema(map[string]any{
		"summary": stringProp("Review summary"), "known_limitations": arrayProp("Known limitations"),
		"idempotency_key": stringProp("Idempotency key"),
	})},
}

func runCheckSchema() map[string]any {
	schema := objectSchema(map[string]any{
		"check_id":        stringProp("Registered required check ID"),
		"idempotency_key": stringProp("Idempotency key"),
	})
	schema["required"] = []string{"check_id", "idempotency_key"}
	return schema
}

func objectSchema(props map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if _, ok := props["idempotency_key"]; ok {
		schema["required"] = []string{"idempotency_key"}
	}
	return schema
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func arrayProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func numberProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func ValidateTool(name string) error {
	for _, t := range Tools {
		if t.Name == name {
			return nil
		}
	}
	return fmt.Errorf("unknown tool %q", name)
}

func ValidateSocketTool(name string) error {
	if name == InternalHookEventTool {
		return nil
	}
	return ValidateTool(name)
}

// ValidateHookEventType limits non-authoritative hook telemetry to fixed event
// names. Hook payloads are deliberately not accepted because repository code
// and provider output are untrusted and may contain source or credentials.
func ValidateHookEventType(name string) error {
	switch name {
	case "session-start", "session-end", "pre-tool", "post-tool", "tool-error",
		"stop", "subagent-stop", "notification", "user-prompt-submit":
		return nil
	default:
		return fmt.Errorf("unsupported hook event type %q", name)
	}
}
