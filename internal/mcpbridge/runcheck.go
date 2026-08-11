package mcpbridge

var runCheckToolDef = ToolDef{
	Name:        "platform_run_check",
	Description: "Runs a registered required check on demand inside the job sandbox. Iterative results never satisfy final required checks.",
	InputSchema: runCheckSchema(),
}

func runCheckSchema() map[string]any {
	schema := objectSchema(map[string]any{
		"check_id":        stringProp("Registered required check ID"),
		"idempotency_key": stringProp("Idempotency key"),
	})
	schema["required"] = []string{"check_id", "idempotency_key"}
	return schema
}
