package mcpbridge

import "testing"

func TestReviewReadySchemaAdvertisesOnlyReviewerContext(t *testing.T) {
	schema := reviewReadyInputSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("review readiness schema is not closed")
	}
	required := schema["required"].([]string)
	for _, field := range []string{"summary", "idempotency_key"} {
		if !containsString(required, field) {
			t.Fatalf("required fields = %v", required)
		}
	}
	properties := schema["properties"].(map[string]any)
	for _, forbidden := range []string{
		"commit_sha", "commit_set", "evidence", "approved", "merged", "waivers",
	} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("schema exposes model-controlled %s", forbidden)
		}
	}
	summary := properties["summary"].(map[string]any)
	if summary["minLength"] != 1 || summary["maxLength"] != 16*1024 {
		t.Fatalf("summary bounds = %v", summary)
	}
	limitations := properties["known_limitations"].(map[string]any)
	if limitations["maxItems"] != 128 {
		t.Fatalf("limitation bounds = %v", limitations)
	}
}
