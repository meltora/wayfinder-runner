package mcpbridge

import (
	"testing"
	"time"
)

func TestToolCallTimeoutCoversTheLongestHumanInputWait(t *testing.T) {
	if MaxHumanInputWait != 7*time.Hour+30*time.Minute {
		t.Fatalf("MaxHumanInputWait = %v", MaxHumanInputWait)
	}
	if got := ToolCallTimeout(HumanInputTool); got != MaxHumanInputWait+2*time.Minute {
		t.Fatalf("human input call timeout = %v", got)
	}
	for _, tool := range []string{"platform_report_progress", "platform_publish_test_results", "platform_get_job_context"} {
		if got := ToolCallTimeout(tool); got != 31*time.Minute {
			t.Fatalf("%s call timeout = %v", tool, got)
		}
	}
}

func TestToolCallTimeoutForDeployPreview(t *testing.T) {
	if DeployPreviewTool != "platform_deploy_preview" {
		t.Fatalf("DeployPreviewTool = %q", DeployPreviewTool)
	}
	if got := ToolCallTimeout(DeployPreviewTool); got != 50*time.Minute {
		t.Fatalf("deploy preview call timeout = %v", got)
	}
}

func TestToolDefinitionsIncludeDeployPreviewWithEmptySchema(t *testing.T) {
	var found *ToolDef
	definitions := ToolDefinitions()
	for index := range definitions {
		if definitions[index].Name == DeployPreviewTool {
			found = &definitions[index]
		}
	}
	if found == nil {
		t.Fatal("platform_deploy_preview is not defined")
	}
	schema, ok := found.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("schema type = %T", found.InputSchema)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema = %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 0 {
		t.Fatalf("properties = %#v", schema["properties"])
	}
	if _, exists := properties["idempotency_key"]; exists {
		t.Fatal("schema requires an idempotency key")
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 0 {
		t.Fatalf("required = %#v", schema["required"])
	}
}
