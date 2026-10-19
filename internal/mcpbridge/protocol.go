package mcpbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"

	"github.com/example/app-runner/internal/hookevent"
)

func decodeStrict(data []byte, out any) error {
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		return err
	}
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

func validateNoDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var consumeValue func() error
	consumeValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("JSON object key is invalid")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON object key is not allowed")
				}
				seen[key] = struct{}{}
				if err := consumeValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := consumeValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
	}
	if err := consumeValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON is not allowed")
		}
		return err
	}
	return nil
}

func DecodeToolObject(data json.RawMessage) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var object map[string]any
	if err := decodeStrict(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("tool arguments must be an object")
	}
	return object, nil
}

const (
	InternalHookEventTool = "platform_record_hook_event"
	InternalListToolsTool = "platform_list_attempt_tools"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
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

var toolDefinitions = []ToolDef{
	{Name: "platform_get_job_context", Description: "Returns approved ticket context, plan status, repository roles, and execution boundaries for the current job.", InputSchema: objectSchema(map[string]any{})},
	{Name: "platform_publish_plan", Description: "Publishes one complete immutable plan for human review. Available only to an initial planning manifest.", InputSchema: planPublicationInputSchema(false)},
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
	{Name: "platform_publish_plan_revision", Description: "Publishes one complete replacement plan bound to the exact reviewed base revision. Available only to a revision planning manifest.", InputSchema: planPublicationInputSchema(true)},
	{Name: "platform_report_blocker", Description: "Reports a typed blocker and whether work can safely continue elsewhere.", InputSchema: objectSchema(map[string]any{
		"blocker_type": stringProp("Blocker type"), "impact": stringProp("Impact"),
		"attempted_resolution": stringProp("What was tried"), "recommended_action": stringProp("Recommended human or system action"),
		"can_continue": boolProp("Can safely continue elsewhere"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: HumanInputTool, Description: "Creates a decision request for a human and blocks until the human answers, the timeout elapses, or the job is cancelled.", InputSchema: objectSchema(map[string]any{
		"question": stringProp("Decision needed"), "options": arrayProp("Options"),
		"recommendation": stringProp("Recommended option"), "consequence_of_delay": stringProp("Consequence of delay"),
		"blocked_operation": stringProp("Exact operation waiting"),
		"timeout_seconds":   numberProp("Max seconds to wait (default 27000, max 27000)"),
		"idempotency_key":   stringProp("Idempotency key (also used as the question id)"),
	})},
	{Name: "platform_publish_decision", Description: "Records an agent-observed implementation decision. Does not replace human approval.", InputSchema: objectSchema(map[string]any{
		"decision": stringProp("Decision made"), "rationale": stringProp("Rationale"),
		"evidence": arrayProp("Supporting evidence references"), "idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_publish_test_results", Description: "Publishes test suite results with provenance.", InputSchema: objectSchema(map[string]any{
		"suite": stringProp("Suite identifier using only letters, numbers, '.', '_' or '-', for example unit"), "command": stringProp("Command run"),
		"result": stringProp("passed|failed|error"), "duration_ms": numberProp("Duration ms"),
		"failures": arrayProp("Failure summaries"), "commit_sha": stringProp("Full lowercase Git commit SHA tested; omit when the change is not committed yet"),
		"idempotency_key": stringProp("Idempotency key"),
	})},
	{Name: "platform_publish_manual_test_plan", Description: "Publishes manual validation steps for a human reviewer.", InputSchema: manualTestPlanInputSchema()},
	{Name: "platform_publish_preview", Description: "Publishes preview environment information bound to the runner-derived commit set.", InputSchema: previewInputSchema()},
	{Name: "platform_publish_artifact", Description: "Uploads a file from the job workspace as a registered artifact.", InputSchema: objectSchema(map[string]any{
		"name": stringProp("Artifact name"), "path": stringProp("Path inside the job workspace"),
		"content_type": stringProp("MIME type"), "idempotency_key": stringProp("Idempotency key"),
	})},
	runCheckToolDef,
	{Name: "platform_mark_review_ready", Description: "Records review intent. The runner publishes a final request only after committing, checking, and pushing the exact change set; the control plane independently verifies evidence.", InputSchema: reviewReadyInputSchema()},
}

func ToolDefinitions() []ToolDef {
	definitions := make([]ToolDef, 0, len(toolDefinitions))
	for _, definition := range toolDefinitions {
		definitions = append(definitions, ToolDef{
			Name: definition.Name, Description: definition.Description,
			InputSchema: cloneToolSchema(definition.InputSchema),
		})
	}
	return definitions
}

func ToolDefinitionsForNames(names []string) ([]ToolDef, error) {
	byName := make(map[string]ToolDef, len(toolDefinitions))
	for _, definition := range toolDefinitions {
		byName[definition.Name] = definition
	}
	definitions := make([]ToolDef, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		definition, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown tool definition")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("duplicate tool definition")
		}
		seen[name] = struct{}{}
		definitions = append(definitions, ToolDef{
			Name: definition.Name, Description: definition.Description,
			InputSchema: cloneToolSchema(definition.InputSchema),
		})
	}
	return definitions, nil
}

func cloneToolSchema(value any) any {
	switch current := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(current))
		for key, nested := range current {
			clone[key] = cloneToolSchema(nested)
		}
		return clone
	case []any:
		clone := make([]any, len(current))
		for index, nested := range current {
			clone[index] = cloneToolSchema(nested)
		}
		return clone
	case []string:
		return append([]string(nil), current...)
	default:
		return current
	}
}

func ToolNamesFromResult(value any) ([]string, error) {
	var values []any
	switch current := value.(type) {
	case []any:
		values = current
	case []string:
		values = make([]any, 0, len(current))
		for _, name := range current {
			values = append(values, name)
		}
	default:
		return nil, fmt.Errorf("attempt tool list is invalid")
	}
	if len(values) > len(toolDefinitions) {
		return nil, fmt.Errorf("attempt tool list is invalid")
	}
	names := make([]string, 0, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if !ok || len(name) == 0 || len(name) > 128 {
			return nil, fmt.Errorf("attempt tool list is invalid")
		}
		names = append(names, name)
	}
	if _, err := ToolDefinitionsForNames(names); err != nil {
		return nil, err
	}
	return names, nil
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
	for _, t := range toolDefinitions {
		if t.Name == name {
			return nil
		}
	}
	return fmt.Errorf("unknown tool %q", name)
}

func ValidateSocketTool(name string) error {
	if name == InternalHookEventTool || name == InternalListToolsTool {
		return nil
	}
	return ValidateTool(name)
}

func ValidateRequestID(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("request id is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("request id is invalid")
	}
	switch id := value.(type) {
	case string:
		if len(id) == 0 || len(id) > 128 {
			return fmt.Errorf("request id is invalid")
		}
	case json.Number:
		number, err := id.Int64()
		if err != nil || math.Abs(float64(number)) > 9_007_199_254_740_991 {
			return fmt.Errorf("request id is invalid")
		}
	default:
		return fmt.Errorf("request id is invalid")
	}
	return nil
}

// ValidateHookEventType limits non-authoritative hook telemetry to fixed event
// names. Hook payloads are deliberately not accepted because repository code
// and provider output are untrusted and may contain source or credentials.
func ValidateHookEventType(name string) error {
	return hookevent.ValidateType(name)
}
