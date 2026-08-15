package mcpbridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanPublicationToolSchemasRequireTheFullStrictPlan(t *testing.T) {
	const planFields = 19
	for _, name := range []string{"platform_publish_plan", "platform_publish_plan_revision"} {
		t.Run(name, func(t *testing.T) {
			var definition *ToolDef
			definitions := ToolDefinitions()
			for index := range definitions {
				if definitions[index].Name == name {
					definition = &definitions[index]
				}
			}
			if definition == nil {
				t.Fatalf("%s is not registered", name)
			}
			schema := definition.InputSchema.(map[string]any)
			if schema["additionalProperties"] != false {
				t.Fatalf("tool schema is not closed: %v", schema)
			}
			properties := schema["properties"].(map[string]any)
			plan := properties["plan"].(map[string]any)
			if plan["additionalProperties"] != false || len(plan["properties"].(map[string]any)) != planFields {
				t.Fatalf("plan schema is not the full closed contract: %v", plan)
			}
			required := schema["required"].([]string)
			if !containsString(required, "plan") || !containsString(required, "change_summary") || !containsString(required, "idempotency_key") {
				t.Fatalf("required fields = %v", required)
			}
			if name == "platform_publish_plan_revision" && !containsString(required, "base_revision_id") {
				t.Fatalf("revision base is optional in schema: %v", required)
			}
		})
	}
	if maxSocketRequestBytes < 1<<20 {
		t.Fatalf("MCP request boundary %d cannot carry the bounded full plan contract", maxSocketRequestBytes)
	}
	if maxSocketResponseBytes < 2<<20 {
		t.Fatalf("MCP response boundary %d cannot carry revision context", maxSocketResponseBytes)
	}
}

func TestUnixBridgeCarriesMaximumPlanRequestsAndRevisionContextResponses(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "mcp-plan-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "plan.sock")
	response := strings.Repeat("r", 1200<<10)
	requestBytes := make(chan int, 1)
	server := &UnixServer{
		SocketPath: socket, Token: "job-token",
		Handler: func(_ context.Context, _ string, arguments json.RawMessage) (any, error) {
			requestBytes <- len(arguments)
			return map[string]any{"revision_context": response}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	request := json.RawMessage(`{"plan":"` + strings.Repeat("p", 768<<10) + `"}`)
	result, err := (&UnixClient{SocketPath: socket, Token: "job-token"}).Call(ctx, publishPlanToolNameForTest, request)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)["revision_context"].(string)
	if len(got) != len(response) {
		t.Fatalf("response bytes=%d, want %d", len(got), len(response))
	}
	if received := <-requestBytes; received < 768<<10 {
		t.Fatalf("request bytes=%d", received)
	}
}

const publishPlanToolNameForTest = "platform_publish_plan"

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
