package scanner

import (
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestRequiresApprovedRuntimeSnapshot(t *testing.T) {
	tests := []struct {
		name      string
		inventory Inventory
		want      bool
	}{
		{name: "empty inventory", inventory: Inventory{}, want: false},
		{
			name: "low risk instructions do not require approval",
			inventory: Inventory{Items: []v1.AgentConfigDiscoveryItem{{
				Path: "AGENTS.md", Decision: v1.AgentConfigDecisionNotRequired,
			}}},
			want: false,
		},
		{
			name: "pending material requires approval",
			inventory: Inventory{Items: []v1.AgentConfigDiscoveryItem{{
				Path: ".mcp.json", Decision: v1.AgentConfigDecisionPending,
			}}},
			want: true,
		},
		{
			name: "rejected material fails closed",
			inventory: Inventory{Items: []v1.AgentConfigDiscoveryItem{{
				Path: ".env", Decision: v1.AgentConfigDecisionRejected,
			}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiresApprovedRuntimeSnapshot(tt.inventory); got != tt.want {
				t.Fatalf("RequiresApprovedRuntimeSnapshot() = %v, want %v", got, tt.want)
			}
		})
	}
}
