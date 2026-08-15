package v1

import "time"

const (
	EventKindAgentConfigDiscovery = "agent_config_discovery"
	AgentConfigCatalogVersion     = "catalog-v1"
)

type AgentConfigSourceType string

const (
	AgentConfigRepositoryInstructions AgentConfigSourceType = "repository_instructions"
	AgentConfigSkill                  AgentConfigSourceType = "skill"
	AgentConfigCommand                AgentConfigSourceType = "command"
	AgentConfigAgentDefinition        AgentConfigSourceType = "agent_definition"
	AgentConfigProviderConfiguration  AgentConfigSourceType = "provider_configuration"
	AgentConfigMCPDeclaration         AgentConfigSourceType = "mcp_declaration"
	AgentConfigHook                   AgentConfigSourceType = "hook"
	AgentConfigDevelopmentInstruction AgentConfigSourceType = "development_instruction"
	AgentConfigPlatformManifest       AgentConfigSourceType = "platform_manifest"
	AgentConfigSymlink                AgentConfigSourceType = "symlink"
	AgentConfigCredential             AgentConfigSourceType = "credential"
	AgentConfigAuthCache              AgentConfigSourceType = "auth_cache"
	AgentConfigBinary                 AgentConfigSourceType = "binary"
	AgentConfigExecutableExtension    AgentConfigSourceType = "executable_extension"
)

type AgentConfigClassification string

const (
	AgentConfigClassReady    AgentConfigClassification = "ready"
	AgentConfigClassWarning  AgentConfigClassification = "warning"
	AgentConfigClassBlocked  AgentConfigClassification = "blocked"
	AgentConfigClassReview   AgentConfigClassification = "review"
	AgentConfigClassRejected AgentConfigClassification = "rejected"
)

type AgentConfigRisk string

const (
	AgentConfigRiskLow      AgentConfigRisk = "low"
	AgentConfigRiskMedium   AgentConfigRisk = "medium"
	AgentConfigRiskHigh     AgentConfigRisk = "high"
	AgentConfigRiskCritical AgentConfigRisk = "critical"
)

type AgentConfigCompatibility string

const (
	AgentConfigCompatible   AgentConfigCompatibility = "compatible"
	AgentConfigDegraded     AgentConfigCompatibility = "degraded"
	AgentConfigIncompatible AgentConfigCompatibility = "incompatible"
	AgentConfigUnknown      AgentConfigCompatibility = "unknown"
)

type AgentConfigDecision string

const (
	AgentConfigDecisionPending     AgentConfigDecision = "pending"
	AgentConfigDecisionApproved    AgentConfigDecision = "approved"
	AgentConfigDecisionRejected    AgentConfigDecision = "rejected"
	AgentConfigDecisionBlocked     AgentConfigDecision = "blocked"
	AgentConfigDecisionNotRequired AgentConfigDecision = "not_required"
)

type AgentConfigDiscoveryItem struct {
	Path                 string                    `json:"path"`
	SourceType           AgentConfigSourceType     `json:"source_type"`
	DigestSHA256         string                    `json:"digest_sha256,omitempty"`
	SizeBytes            int64                     `json:"size_bytes"`
	Classification       AgentConfigClassification `json:"classification"`
	Risk                 AgentConfigRisk           `json:"risk"`
	Compatibility        AgentConfigCompatibility  `json:"compatibility"`
	Decision             AgentConfigDecision       `json:"decision"`
	Provider             string                    `json:"provider,omitempty"`
	RequestedPermissions []string                  `json:"requested_permissions"`
	Conflicts            []string                  `json:"conflicts"`
	Reason               string                    `json:"reason,omitempty"`
}

type AgentConfigDiscoveryEvent struct {
	Kind           string                     `json:"kind"`
	ScanID         string                     `json:"scan_id"`
	ProjectID      string                     `json:"project_id"`
	RepositoryID   string                     `json:"repository_id"`
	BranchRef      string                     `json:"branch_ref"`
	CommitSHA      string                     `json:"commit_sha"`
	CatalogVersion string                     `json:"catalog_version"`
	ParserVersion  string                     `json:"parser_version"`
	ObservedAt     time.Time                  `json:"observed_at"`
	Items          []AgentConfigDiscoveryItem `json:"items"`
}
