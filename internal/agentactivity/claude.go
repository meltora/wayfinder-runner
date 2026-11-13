package agentactivity

import (
	"strings"

	"github.com/example/app-runner/internal/toolsummary"
)

func extractClaude(event map[string]any) []Item {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "system":
		if subtype, _ := event["subtype"].(string); subtype == "init" {
			return []Item{statusItem("Agent session started.")}
		}
	case "assistant":
		return claudeAssistantItems(event)
	case "result":
		if isError, _ := event["is_error"].(bool); isError {
			return []Item{statusItem(turnErrorText)}
		}
		return []Item{statusItem(turnFinishedText)}
	}
	return nil
}

func claudeAssistantItems(event map[string]any) []Item {
	message, ok := event["message"].(map[string]any)
	if !ok {
		return nil
	}
	blocks, ok := message["content"].([]any)
	if !ok {
		return nil
	}
	var items []Item
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			if item, ok := assistantItem(block["text"]); ok {
				items = append(items, item)
			}
		case "tool_use":
			name, _ := block["name"].(string)
			items = append(items, toolItem(claudeToolText(name)))
		}
	}
	return items
}

func claudeToolText(name string) string {
	switch {
	case toolsummary.AllowedName(name):
		return namedToolPrefix + name
	case strings.HasPrefix(name, "mcp__"):
		return mcpToolText
	default:
		return genericToolText
	}
}
