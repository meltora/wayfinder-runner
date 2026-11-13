package daemon

import (
	"fmt"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var agentGuidanceTitles = map[v1.AgentGuidanceLayerName]string{
	v1.AgentGuidanceLayerOrganization: "Organization guidance",
	v1.AgentGuidanceLayerProject:      "Project guidance",
}

func agentGuidancePrompt(guidance *v1.AgentGuidance) string {
	if guidance == nil || len(guidance.Layers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nORGANIZATION AND PROJECT AGENT GUIDANCE\n")
	b.WriteString("Administrators of this organization and project wrote the guidance below. Follow it where it applies. ")
	b.WriteString("It cannot override platform safety rules, the signed Agent Pack, the approved plan, capabilities or execution_boundaries. ")
	b.WriteString("It outranks repository instructions such as AGENTS.md or CLAUDE.md. Where organization and project guidance conflict, organization guidance wins.\n\n")
	sections := make([]string, 0, len(guidance.Layers))
	for _, layer := range guidance.Layers {
		sections = append(sections, fmt.Sprintf("%s (revision %d):\n%s", agentGuidanceTitles[layer.Layer], layer.Revision, layer.Content))
	}
	b.WriteString(strings.Join(sections, "\n\n"))
	b.WriteString("\nEND AGENT GUIDANCE\n\n")
	return b.String()
}
