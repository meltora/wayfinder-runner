package mcpbridge

func reviewReadyInputSchema() map[string]any {
	return closedObjectSchema(map[string]any{
		"summary": map[string]any{
			"type": "string", "description": "Bounded reviewer context; not evidence or approval",
			"minLength": 1, "maxLength": 16 * 1024,
		},
		"known_limitations": map[string]any{
			"type": "array", "description": "Known reviewer-visible limitations",
			"maxItems": 128,
			"items":    map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		},
		"idempotency_key": map[string]any{
			"type": "string", "description": "Stable review-intent key for this attempt",
			"minLength": 1, "maxLength": 128, "pattern": `^[A-Za-z0-9][A-Za-z0-9._-]*$`,
		},
	}, []string{"summary", "idempotency_key"})
}
