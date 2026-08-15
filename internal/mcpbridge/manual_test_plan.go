package mcpbridge

func manualTestPlanInputSchema() map[string]any {
	instructions := func(description string, minimum int) map[string]any {
		return map[string]any{
			"type": "array", "description": description,
			"minItems": minimum, "maxItems": 128,
			"items": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		}
	}
	return closedObjectSchema(map[string]any{
		"steps":            instructions("Ordered steps", 1),
		"prerequisites":    instructions("Prerequisites", 0),
		"expected_results": instructions("Expected results", 1),
		"cleanup": map[string]any{
			"type": "string", "description": "Cleanup instructions", "maxLength": 8192,
		},
		"idempotency_key": stringProp("Idempotency key"),
	}, []string{"steps", "expected_results", "idempotency_key"})
}
