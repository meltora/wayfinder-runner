package agentactivity

func extractCodex(event map[string]any) []Item {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "item.completed":
		item, ok := event["item"].(map[string]any)
		if !ok {
			return nil
		}
		return codexItem(item)
	case "turn.completed":
		return []Item{statusItem(turnFinishedText)}
	case "turn.failed":
		return []Item{statusItem(turnErrorText)}
	}
	return nil
}

func codexItem(item map[string]any) []Item {
	switch item["type"] {
	case "agent_message":
		if value, ok := assistantItem(item["text"]); ok {
			return []Item{value}
		}
	case "command_execution":
		return []Item{toolItem("Ran a shell command")}
	case "file_change":
		return []Item{toolItem("Changed files")}
	case "mcp_tool_call":
		return []Item{toolItem(mcpToolText)}
	case "web_search":
		return []Item{toolItem("Searched the web")}
	case "todo_list":
		return []Item{toolItem("Updated the task list")}
	}
	return nil
}
