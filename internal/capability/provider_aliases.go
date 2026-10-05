package capability

import v1 "github.com/example/app-runner/api/runner/v1"

// providerCapabilityAliases publishes two identifiers for the same Claude
// adapter. Both entries reuse one probe and do not represent extra capacity.
func providerCapabilityAliases(probed v1.AgentCapability) []v1.AgentCapability {
	legacy := probed
	legacy.ManagedHookEvents = append([]string(nil), probed.ManagedHookEvents...)
	entries := []v1.AgentCapability{legacy}
	if probed.Provider == "claude" {
		canonical := probed
		canonical.Provider = "claude-code"
		canonical.ManagedHookEvents = append([]string(nil), probed.ManagedHookEvents...)
		entries = append(entries, canonical)
	}
	return entries
}
