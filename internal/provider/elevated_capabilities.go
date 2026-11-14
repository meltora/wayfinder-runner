package provider

import (
	"sort"

	"github.com/example/app-runner/internal/elevatedcapability"
)

func (p StartParams) elevatedGrants() elevatedcapability.Grants {
	if p.ReadOnly {
		return elevatedcapability.Grants{}
	}
	return p.Elevated
}

func claudeMCPServers(platform map[string]any, grants elevatedcapability.Grants) map[string]any {
	servers := map[string]any{"platform": platform}
	for name, server := range grants.MCPServers {
		if name != "platform" {
			servers[name] = server
		}
	}
	return servers
}

func claudeElevatedAllowedTools(grants elevatedcapability.Grants) []string {
	tools := []string{}
	for _, name := range grants.MCPServerNames() {
		if name != "platform" {
			tools = append(tools, "mcp__"+name+"__*")
		}
	}
	return tools
}

func claudeAllowedDomains(grants elevatedcapability.Grants) []string {
	domains := append([]string{}, grants.AllowedDomains...)
	sort.Strings(domains)
	return domains
}

func withApprovedHooks(managed map[string]any, approved map[string][]map[string]any) map[string]any {
	if len(approved) == 0 {
		return managed
	}
	merged := make(map[string]any, len(managed)+len(approved))
	for event, groups := range managed {
		merged[event] = groups
	}
	events := make([]string, 0, len(approved))
	for event := range approved {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		combined := []any{}
		switch existing := merged[event].(type) {
		case []map[string]any:
			for _, group := range existing {
				combined = append(combined, group)
			}
		case []any:
			combined = append(combined, existing...)
		}
		for _, group := range approved[event] {
			combined = append(combined, group)
		}
		merged[event] = combined
	}
	return merged
}
