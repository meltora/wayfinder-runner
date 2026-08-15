package scanner

import v1 "github.com/example/app-runner/api/runner/v1"

func classifiedItem(path string, match catalogMatch, size int64) v1.AgentConfigDiscoveryItem {
	item := v1.AgentConfigDiscoveryItem{
		Path: path, SourceType: match.sourceType, SizeBytes: size, Provider: match.provider,
		RequestedPermissions: []string{}, Conflicts: []string{},
	}
	switch match.sourceType {
	case v1.AgentConfigRepositoryInstructions, v1.AgentConfigSkill, v1.AgentConfigDevelopmentInstruction:
		item.Classification = v1.AgentConfigClassReady
		item.Risk = v1.AgentConfigRiskLow
		item.Compatibility = v1.AgentConfigCompatible
		item.Decision = v1.AgentConfigDecisionNotRequired
	case v1.AgentConfigCommand, v1.AgentConfigAgentDefinition:
		item.Classification = v1.AgentConfigClassReview
		item.Risk = v1.AgentConfigRiskMedium
		item.Compatibility = v1.AgentConfigUnknown
		item.Decision = v1.AgentConfigDecisionPending
		item.Reason = "agent behavior extension requires review"
	case v1.AgentConfigProviderConfiguration:
		item.Classification = v1.AgentConfigClassReview
		item.Risk = v1.AgentConfigRiskHigh
		item.Compatibility = v1.AgentConfigUnknown
		item.Decision = v1.AgentConfigDecisionPending
		item.RequestedPermissions = []string{"provider.configure"}
		item.Reason = "provider configuration requires review"
	case v1.AgentConfigMCPDeclaration:
		item.Classification = v1.AgentConfigClassReview
		item.Risk = v1.AgentConfigRiskHigh
		item.Compatibility = v1.AgentConfigUnknown
		item.Decision = v1.AgentConfigDecisionPending
		item.RequestedPermissions = []string{"network.connect"}
		item.Reason = "MCP declaration requires review"
	case v1.AgentConfigHook:
		item.Classification = v1.AgentConfigClassReview
		item.Risk = v1.AgentConfigRiskHigh
		item.Compatibility = v1.AgentConfigUnknown
		item.Decision = v1.AgentConfigDecisionPending
		item.RequestedPermissions = []string{"process.execute"}
		item.Reason = "hook execution requires review"
	case v1.AgentConfigPlatformManifest:
		item.Classification = v1.AgentConfigClassReview
		item.Risk = v1.AgentConfigRiskHigh
		item.Compatibility = v1.AgentConfigUnknown
		item.Decision = v1.AgentConfigDecisionPending
		item.RequestedPermissions = []string{"platform.configure"}
		item.Reason = "platform manifest requires review"
	case v1.AgentConfigExecutableExtension:
		item.Classification = v1.AgentConfigClassReview
		item.Risk = v1.AgentConfigRiskHigh
		item.Compatibility = v1.AgentConfigUnknown
		item.Decision = v1.AgentConfigDecisionPending
		item.RequestedPermissions = []string{"process.execute"}
		item.Reason = "executable extension requires review"
	default:
		return rejectedItem(path, match.sourceType, size, match.provider, "unsafe repository content is never imported")
	}
	return item
}

func rejectedItem(path string, sourceType v1.AgentConfigSourceType, size int64, provider, reason string) v1.AgentConfigDiscoveryItem {
	return v1.AgentConfigDiscoveryItem{
		Path: path, SourceType: sourceType, SizeBytes: size, Provider: provider,
		Classification: v1.AgentConfigClassRejected, Risk: v1.AgentConfigRiskCritical,
		Compatibility: v1.AgentConfigIncompatible, Decision: v1.AgentConfigDecisionRejected,
		RequestedPermissions: []string{}, Conflicts: []string{}, Reason: reason,
	}
}
