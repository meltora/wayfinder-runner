package agentactivity

import v1 "github.com/example/app-runner/api/runner/v1"

type Item struct {
	Type v1.AgentActivityType
	Text string
}

const (
	turnFinishedText = "Agent turn finished."
	turnErrorText    = "Agent turn ended with an error."
	mcpToolText      = "Using an MCP tool"
	genericToolText  = "Using a tool"
	namedToolPrefix  = "Using tool: "
)

func assistantItem(value any) (Item, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return Item{}, false
	}
	return Item{Type: v1.AgentActivityAssistantMessage, Text: text}, true
}

func statusItem(text string) Item {
	return Item{Type: v1.AgentActivityStatus, Text: text}
}

func toolItem(text string) Item {
	return Item{Type: v1.AgentActivityToolStep, Text: text}
}
