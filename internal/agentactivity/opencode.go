package agentactivity

var openCodeToolNames = map[string]struct{}{
	"bash": {}, "edit": {}, "glob": {}, "grep": {}, "list": {}, "patch": {}, "read": {},
	"task": {}, "todoread": {}, "todowrite": {}, "webfetch": {}, "write": {},
}

func extractOpenCode(event map[string]any) []Item {
	part, ok := event["part"].(map[string]any)
	if !ok {
		return nil
	}
	eventType, _ := event["type"].(string)
	switch eventType {
	case "text":
		if item, ok := assistantItem(part["text"]); ok {
			return []Item{item}
		}
	case "tool_use":
		name, _ := part["tool"].(string)
		if _, allowed := openCodeToolNames[name]; allowed {
			return []Item{toolItem(namedToolPrefix + name)}
		}
		return []Item{toolItem(genericToolText)}
	case "step_finish":
		return []Item{statusItem("Agent step finished.")}
	}
	return nil
}
