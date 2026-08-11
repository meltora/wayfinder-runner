package mcpbridge

import "testing"

func TestRunCheckToolSchema(t *testing.T) {
	var def *ToolDef
	for i := range Tools {
		if Tools[i].Name == "platform_run_check" {
			def = &Tools[i]
		}
	}
	if def == nil {
		t.Fatal("platform_run_check is not registered")
	}
	if err := ValidateSocketTool("platform_run_check"); err != nil {
		t.Fatalf("platform_run_check is not a valid socket tool: %v", err)
	}
	schema, ok := def.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("schema has unexpected type %T", def.InputSchema)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema must be a closed object: %v", schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties missing")
	}
	if len(props) != 2 || props["check_id"] == nil || props["idempotency_key"] == nil {
		t.Fatalf("platform_run_check must accept only check_id and idempotency_key: %v", props)
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 {
		t.Fatalf("required = %v", schema["required"])
	}
	seen := map[string]bool{}
	for _, field := range required {
		seen[field] = true
	}
	if !seen["check_id"] || !seen["idempotency_key"] {
		t.Fatalf("required = %v", required)
	}
}
