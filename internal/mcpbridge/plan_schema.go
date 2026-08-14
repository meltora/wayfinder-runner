package mcpbridge

import "slices"

func planPublicationInputSchema(revision bool) map[string]any {
	properties := map[string]any{
		"plan":            planContentInputSchema(),
		"change_summary":  stringProp("What this plan establishes or changes and why"),
		"idempotency_key": stringProp("Stable publication key for this planning attempt"),
	}
	required := []string{"plan", "change_summary", "idempotency_key"}
	if revision {
		properties["base_revision_id"] = stringProp("Exact reviewed plan revision ID from plan_revision_context")
		required = append(required, "base_revision_id")
	}
	return closedObjectSchema(properties, required)
}

func planContentInputSchema() map[string]any {
	properties := map[string]any{
		"behavior":                stringProp("User-visible behavior and acceptance interpretation"),
		"in_scope":                arrayProp("Included work"),
		"out_of_scope":            arrayProp("Explicit exclusions"),
		"assumptions":             arrayProp("Assumptions"),
		"unanswered_questions":    arrayProp("Questions still requiring an answer"),
		"architecture":            stringProp("Technical architecture and component changes"),
		"api_impact":              stringProp("API and contract impact"),
		"data_impact":             stringProp("Schema, migration, and data impact"),
		"security_impact":         stringProp("Threats, controls, and authorization impact"),
		"operational_impact":      stringProp("Runtime, observability, and support impact"),
		"preview_requirements":    stringProp("Preview environment requirements"),
		"automated_test_strategy": stringProp("Automated unit, integration, and end-to-end tests"),
		"manual_test_plan":        stringProp("Manual developer and reviewer checks"),
		"rollout_strategy":        stringProp("Deployment and rollout strategy"),
		"rollback_strategy":       stringProp("Rollback and recovery strategy"),
		"risks":                   arrayProp("Known risks"),
		"alternatives":            arrayProp("Alternatives considered"),
		"dependencies":            arrayProp("Dependencies and prerequisites"),
		"delivery_order":          arrayProp("Ordered implementation slices"),
	}
	required := make([]string, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	slices.Sort(required)
	return closedObjectSchema(properties, required)
}

func closedObjectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type": "object", "properties": properties,
		"required": required, "additionalProperties": false,
	}
}
