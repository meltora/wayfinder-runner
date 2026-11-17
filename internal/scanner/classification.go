package scanner

import (
	"path"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var rejectionReasons = map[v1.AgentConfigPackReasonCode]string{
	v1.AgentConfigPackReasonUnreadableDuringScan: "file changed or could not be read safely",
	v1.AgentConfigPackReasonExceedsScanSizeLimit: "file exceeds the metadata scan size limit",
	v1.AgentConfigPackReasonSymlink:              "symlinks are never imported",
	v1.AgentConfigPackReasonSpecialFile:          "special files are never imported",
	v1.AgentConfigPackReasonBinaryContent:        "binary content is never imported",
	v1.AgentConfigPackReasonCredentialFile:       "unsafe repository content is never imported",
	v1.AgentConfigPackReasonSecretContent:        "secret-like content is never imported",
	v1.AgentConfigPackReasonAuthCache:            "unsafe repository content is never imported",
	v1.AgentConfigPackReasonUnsafeContent:        "unsafe repository content is never imported",
}

func reviewedItem(item v1.AgentConfigDiscoveryItem, risk v1.AgentConfigRisk, permission, reason string, code v1.AgentConfigPackReasonCode) v1.AgentConfigDiscoveryItem {
	item.Classification = v1.AgentConfigClassReview
	item.Risk = risk
	item.Compatibility = v1.AgentConfigUnknown
	item.Decision = v1.AgentConfigDecisionPending
	if permission != "" {
		item.RequestedPermissions = []string{permission}
	}
	item.Reason = reason
	return withPackReason(item, code)
}

func withPackReason(item v1.AgentConfigDiscoveryItem, code v1.AgentConfigPackReasonCode) v1.AgentConfigDiscoveryItem {
	item.PackClass = v1.AgentConfigPackReasonRules[code].Class
	item.PackReasonCode = code
	return item
}

func instructionsReasonCode(relativePath string, sourceType v1.AgentConfigSourceType) v1.AgentConfigPackReasonCode {
	if sourceType == v1.AgentConfigRepositoryInstructions && path.Base(relativePath) == "CLAUDE.md" {
		return v1.AgentConfigPackReasonInstructionsProviderSpecific
	}
	if sourceType == v1.AgentConfigSkill {
		return v1.AgentConfigPackReasonSkillPortable
	}
	return v1.AgentConfigPackReasonInstructionsPortable
}

func classifiedItem(relativePath string, match catalogMatch, size int64) v1.AgentConfigDiscoveryItem {
	item := v1.AgentConfigDiscoveryItem{
		Path: relativePath, SourceType: match.sourceType, SizeBytes: size, Provider: match.provider,
		RequestedPermissions: []string{}, Conflicts: []string{},
	}
	switch match.sourceType {
	case v1.AgentConfigRepositoryInstructions, v1.AgentConfigSkill, v1.AgentConfigDevelopmentInstruction:
		item.Classification = v1.AgentConfigClassReady
		item.Risk = v1.AgentConfigRiskLow
		item.Compatibility = v1.AgentConfigCompatible
		item.Decision = v1.AgentConfigDecisionNotRequired
		return withPackReason(item, instructionsReasonCode(relativePath, match.sourceType))
	case v1.AgentConfigCommand:
		return reviewedItem(item, v1.AgentConfigRiskMedium, "", "agent behavior extension requires review", v1.AgentConfigPackReasonCommandProviderSpecific)
	case v1.AgentConfigAgentDefinition:
		return reviewedItem(item, v1.AgentConfigRiskMedium, "", "agent behavior extension requires review", v1.AgentConfigPackReasonAgentDefinitionProviderSpecific)
	case v1.AgentConfigProviderConfiguration:
		return reviewedItem(item, v1.AgentConfigRiskHigh, "provider.configure", "provider configuration requires review", v1.AgentConfigPackReasonProviderSettings)
	case v1.AgentConfigMCPDeclaration:
		return reviewedItem(item, v1.AgentConfigRiskHigh, "network.connect", "MCP declaration requires review", v1.AgentConfigPackReasonMCPServer)
	case v1.AgentConfigHook:
		return reviewedItem(item, v1.AgentConfigRiskHigh, "process.execute", "hook execution requires review", v1.AgentConfigPackReasonHookProgram)
	case v1.AgentConfigPlatformManifest:
		return reviewedItem(item, v1.AgentConfigRiskHigh, "platform.configure", "platform manifest requires review", v1.AgentConfigPackReasonPlatformManifest)
	case v1.AgentConfigExecutableExtension:
		return reviewedItem(item, v1.AgentConfigRiskHigh, "process.execute", "executable extension requires review", v1.AgentConfigPackReasonExecutableFile)
	case v1.AgentConfigCredential:
		return rejectedItem(relativePath, match.sourceType, size, match.provider, v1.AgentConfigPackReasonCredentialFile)
	case v1.AgentConfigAuthCache:
		return rejectedItem(relativePath, match.sourceType, size, match.provider, v1.AgentConfigPackReasonAuthCache)
	default:
		return rejectedItem(relativePath, match.sourceType, size, match.provider, v1.AgentConfigPackReasonUnsafeContent)
	}
}

func rejectedItem(relativePath string, sourceType v1.AgentConfigSourceType, size int64, provider string, code v1.AgentConfigPackReasonCode) v1.AgentConfigDiscoveryItem {
	return withPackReason(v1.AgentConfigDiscoveryItem{
		Path: relativePath, SourceType: sourceType, SizeBytes: size, Provider: provider,
		Classification: v1.AgentConfigClassRejected, Risk: v1.AgentConfigRiskCritical,
		Compatibility: v1.AgentConfigIncompatible, Decision: v1.AgentConfigDecisionRejected,
		RequestedPermissions: []string{}, Conflicts: []string{}, Reason: rejectionReasons[code],
	}, code)
}
