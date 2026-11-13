package agentactivity

func Extract(provider string, event map[string]any) []Item {
	switch provider {
	case "claude":
		return extractClaude(event)
	case "codex":
		return extractCodex(event)
	case "opencode":
		return extractOpenCode(event)
	default:
		return nil
	}
}
