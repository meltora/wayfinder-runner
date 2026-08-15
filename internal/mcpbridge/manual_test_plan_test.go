package mcpbridge

import "testing"

func TestManualTestPlanSchemaAdvertisesTheRuntimeBoundary(t *testing.T) {
	schema := manualTestPlanInputSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("manual test plan schema is not closed")
	}
	required, ok := schema["required"].([]string)
	if !ok || !containsString(required, "steps") || !containsString(required, "expected_results") || !containsString(required, "idempotency_key") {
		t.Fatalf("required fields = %v", schema["required"])
	}
	properties := schema["properties"].(map[string]any)
	for _, field := range []string{"steps", "prerequisites", "expected_results"} {
		property := properties[field].(map[string]any)
		if property["maxItems"] != 128 {
			t.Fatalf("%s maxItems = %v", field, property["maxItems"])
		}
		item := property["items"].(map[string]any)
		if item["maxLength"] != 4096 {
			t.Fatalf("%s item maxLength = %v", field, item["maxLength"])
		}
	}
}
