package mcpbridge

import "testing"

func TestPreviewSchemaAdvertisesTheRuntimeBoundary(t *testing.T) {
	schema := previewInputSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("preview schema is not closed")
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required fields = %T", schema["required"])
	}
	for _, field := range []string{"url", "health", "expires_at", "idempotency_key"} {
		if !containsString(required, field) {
			t.Fatalf("required fields = %v", required)
		}
	}
	properties := schema["properties"].(map[string]any)
	for _, forbidden := range []string{"commit_sha", "commit_set"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("schema exposes model-controlled %s", forbidden)
		}
	}
	limitations := properties["known_limitations"].(map[string]any)
	if limitations["maxItems"] != 128 {
		t.Fatalf("known_limitations maxItems = %v", limitations["maxItems"])
	}
	item := limitations["items"].(map[string]any)
	if item["maxLength"] != 4096 {
		t.Fatalf("known_limitations item maxLength = %v", item["maxLength"])
	}
}
