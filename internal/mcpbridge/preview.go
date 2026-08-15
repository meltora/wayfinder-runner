package mcpbridge

func previewInputSchema() map[string]any {
	return closedObjectSchema(map[string]any{
		"url": map[string]any{
			"type": "string", "description": "Absolute HTTPS preview URL without credentials, query, or fragment",
			"minLength": 1, "maxLength": 4096, "format": "uri",
		},
		"health": map[string]any{
			"type": "string", "description": "Observed preview health",
			"enum": []string{"healthy", "degraded", "down"},
		},
		"expires_at": map[string]any{
			"type": "string", "description": "Preview expiry as an RFC3339 timestamp",
			"format": "date-time",
		},
		"known_limitations": map[string]any{
			"type": "array", "description": "Known reviewer-visible limitations",
			"maxItems": 128,
			"items":    map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		},
		"idempotency_key": map[string]any{
			"type": "string", "description": "Stable publication key for this attempt",
			"minLength": 1, "maxLength": 128, "pattern": `^[A-Za-z0-9][A-Za-z0-9._-]*$`,
		},
	}, []string{"url", "health", "expires_at", "idempotency_key"})
}
