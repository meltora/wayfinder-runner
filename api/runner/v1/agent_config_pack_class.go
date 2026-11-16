package v1

type AgentConfigPackClass string

const (
	AgentConfigPackPortableGuidance AgentConfigPackClass = "portable_guidance"
	AgentConfigPackProviderOverlay  AgentConfigPackClass = "provider_overlay"
	AgentConfigPackElevated         AgentConfigPackClass = "elevated"
	AgentConfigPackUnsupported      AgentConfigPackClass = "unsupported"
	AgentConfigPackRejected         AgentConfigPackClass = "rejected"
	AgentConfigPackManualReview     AgentConfigPackClass = "manual_review"
)

type AgentConfigPackReasonCode string

const (
	AgentConfigPackReasonInstructionsPortable            AgentConfigPackReasonCode = "instructions_portable"
	AgentConfigPackReasonSkillPortable                   AgentConfigPackReasonCode = "skill_portable"
	AgentConfigPackReasonInstructionsProviderSpecific    AgentConfigPackReasonCode = "instructions_provider_specific"
	AgentConfigPackReasonCommandProviderSpecific         AgentConfigPackReasonCode = "command_provider_specific"
	AgentConfigPackReasonAgentDefinitionProviderSpecific AgentConfigPackReasonCode = "agent_definition_provider_specific"
	AgentConfigPackReasonProviderSettings                AgentConfigPackReasonCode = "provider_settings"
	AgentConfigPackReasonMCPServer                       AgentConfigPackReasonCode = "mcp_server"
	AgentConfigPackReasonHookProgram                     AgentConfigPackReasonCode = "hook_program"
	AgentConfigPackReasonExecutableFile                  AgentConfigPackReasonCode = "executable_file"
	AgentConfigPackReasonPlatformManifest                AgentConfigPackReasonCode = "platform_manifest"
	AgentConfigPackReasonUnreadableDuringScan            AgentConfigPackReasonCode = "unreadable_during_scan"
	AgentConfigPackReasonExceedsScanSizeLimit            AgentConfigPackReasonCode = "exceeds_scan_size_limit"
	AgentConfigPackReasonSymlink                         AgentConfigPackReasonCode = "symlink"
	AgentConfigPackReasonSpecialFile                     AgentConfigPackReasonCode = "special_file"
	AgentConfigPackReasonBinaryContent                   AgentConfigPackReasonCode = "binary_content"
	AgentConfigPackReasonCredentialFile                  AgentConfigPackReasonCode = "credential_file"
	AgentConfigPackReasonSecretContent                   AgentConfigPackReasonCode = "secret_content"
	AgentConfigPackReasonAuthCache                       AgentConfigPackReasonCode = "auth_cache"
	AgentConfigPackReasonUnsafeContent                   AgentConfigPackReasonCode = "unsafe_content"
)

type AgentConfigPackReasonRule struct {
	Class       AgentConfigPackClass
	Rejected    bool
	SourceTypes []AgentConfigSourceType
}

var safeAgentConfigSourceTypes = []AgentConfigSourceType{
	AgentConfigRepositoryInstructions, AgentConfigSkill, AgentConfigCommand,
	AgentConfigAgentDefinition, AgentConfigProviderConfiguration, AgentConfigMCPDeclaration,
	AgentConfigHook, AgentConfigDevelopmentInstruction, AgentConfigPlatformManifest,
	AgentConfigExecutableExtension,
}

var allAgentConfigSourceTypes = append(append([]AgentConfigSourceType{}, safeAgentConfigSourceTypes...),
	AgentConfigSymlink, AgentConfigCredential, AgentConfigAuthCache, AgentConfigBinary)

var AgentConfigPackReasonRules = map[AgentConfigPackReasonCode]AgentConfigPackReasonRule{
	AgentConfigPackReasonInstructionsPortable:            {AgentConfigPackPortableGuidance, false, []AgentConfigSourceType{AgentConfigRepositoryInstructions, AgentConfigDevelopmentInstruction}},
	AgentConfigPackReasonSkillPortable:                   {AgentConfigPackPortableGuidance, false, []AgentConfigSourceType{AgentConfigSkill}},
	AgentConfigPackReasonInstructionsProviderSpecific:    {AgentConfigPackProviderOverlay, false, []AgentConfigSourceType{AgentConfigRepositoryInstructions}},
	AgentConfigPackReasonCommandProviderSpecific:         {AgentConfigPackProviderOverlay, false, []AgentConfigSourceType{AgentConfigCommand}},
	AgentConfigPackReasonAgentDefinitionProviderSpecific: {AgentConfigPackProviderOverlay, false, []AgentConfigSourceType{AgentConfigAgentDefinition}},
	AgentConfigPackReasonProviderSettings:                {AgentConfigPackProviderOverlay, false, []AgentConfigSourceType{AgentConfigProviderConfiguration}},
	AgentConfigPackReasonMCPServer:                       {AgentConfigPackElevated, false, []AgentConfigSourceType{AgentConfigMCPDeclaration}},
	AgentConfigPackReasonHookProgram:                     {AgentConfigPackElevated, false, []AgentConfigSourceType{AgentConfigHook}},
	AgentConfigPackReasonExecutableFile:                  {AgentConfigPackElevated, false, []AgentConfigSourceType{AgentConfigExecutableExtension}},
	AgentConfigPackReasonPlatformManifest:                {AgentConfigPackManualReview, false, []AgentConfigSourceType{AgentConfigPlatformManifest}},
	AgentConfigPackReasonUnreadableDuringScan:            {AgentConfigPackManualReview, true, safeAgentConfigSourceTypes},
	AgentConfigPackReasonExceedsScanSizeLimit:            {AgentConfigPackUnsupported, true, safeAgentConfigSourceTypes},
	AgentConfigPackReasonSymlink:                         {AgentConfigPackRejected, true, []AgentConfigSourceType{AgentConfigSymlink}},
	AgentConfigPackReasonSpecialFile:                     {AgentConfigPackRejected, true, []AgentConfigSourceType{AgentConfigBinary}},
	AgentConfigPackReasonBinaryContent:                   {AgentConfigPackRejected, true, []AgentConfigSourceType{AgentConfigBinary}},
	AgentConfigPackReasonCredentialFile:                  {AgentConfigPackRejected, true, []AgentConfigSourceType{AgentConfigCredential}},
	AgentConfigPackReasonSecretContent:                   {AgentConfigPackRejected, true, []AgentConfigSourceType{AgentConfigCredential}},
	AgentConfigPackReasonAuthCache:                       {AgentConfigPackRejected, true, []AgentConfigSourceType{AgentConfigAuthCache}},
	AgentConfigPackReasonUnsafeContent:                   {AgentConfigPackRejected, true, allAgentConfigSourceTypes},
}
