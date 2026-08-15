package scanner

import v1 "github.com/example/app-runner/api/runner/v1"

// RequiresApprovedRuntimeSnapshot prevents provider CLIs from implicitly
// loading repository-owned configuration before the control plane has applied
// its policy and a human has approved every review-required item.
func RequiresApprovedRuntimeSnapshot(inventory Inventory) bool {
	for _, item := range inventory.Items {
		if item.Decision != v1.AgentConfigDecisionNotRequired {
			return true
		}
	}
	return false
}
