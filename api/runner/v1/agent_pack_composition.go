package v1

import "time"

const (
	EventKindAgentPackComposition                 = "agent_pack_composition"
	AgentPackCompositionSchemaVersion             = 1
	MaxAgentPackCompositionSourcesPerLayer        = 64
	MaxAgentPackCompositionConflicts              = 64
	AgentPackCompositionHostHomeLabel             = "host_home"
	AgentPackCompositionPhaseBeforeStart          = "before_start"
	AgentPackCompositionPhaseAfterExit            = "after_exit"
	AgentPackCompositionIsolationDocker           = "docker"
	AgentPackCompositionIsolationDirect           = "direct"
	AgentPackCompositionPackViewVerified          = "verified"
	AgentPackCompositionPackViewModified          = "modified"
	AgentPackCompositionPackViewAbsent            = "absent"
	AgentPackCompositionLayerPlatformSafety       = "platform_safety"
	AgentPackCompositionLayerAgentPack            = "agent_pack"
	AgentPackCompositionLayerRepository           = "repository_instructions"
	AgentPackCompositionLayerNested               = "nested_instructions"
	AgentPackCompositionLayerExecutionManifest    = "execution_manifest"
	AgentPackCompositionLayerTicketContext        = "ticket_context"
	AgentPackCompositionLayerHostUser             = "host_user_configuration"
	AgentPackCompositionApplied                   = "applied"
	AgentPackCompositionExcluded                  = "excluded"
	AgentPackCompositionAbsent                    = "absent"
	AgentPackCompositionNotComposed               = "not_composed"
	AgentPackCompositionAppliedUnisolated         = "applied_unisolated"
	AgentPackConflictRepositorySettingsExcluded   = "repository_settings_excluded"
	AgentPackConflictRepositoryMCPExcluded        = "repository_mcp_excluded"
	AgentPackConflictSkillNameCollision           = "skill_name_collision"
	AgentPackConflictPeerRepositoryInstructions   = "peer_repository_instructions"
	AgentPackConflictRepositoryManifestNotApplied = "repository_manifest_not_applied"
	AgentPackConflictHostUserConfigurationLoaded  = "host_user_configuration_loaded"
	AgentPackWinnerPlatformSafety                 = "platform_safety"
	AgentPackWinnerAgentPack                      = "agent_pack"
	AgentPackWinnerRepository                     = "repository"
	AgentPackWinnerUnresolved                     = "unresolved"
)

var AgentPackCompositionLayerOrder = []string{
	AgentPackCompositionLayerPlatformSafety,
	AgentPackCompositionLayerAgentPack,
	AgentPackCompositionLayerRepository,
	AgentPackCompositionLayerNested,
	AgentPackCompositionLayerExecutionManifest,
	AgentPackCompositionLayerTicketContext,
	AgentPackCompositionLayerHostUser,
}

var AgentPackCompositionConflictWinners = map[string]string{
	AgentPackConflictRepositorySettingsExcluded:   AgentPackWinnerPlatformSafety,
	AgentPackConflictRepositoryMCPExcluded:        AgentPackWinnerPlatformSafety,
	AgentPackConflictSkillNameCollision:           AgentPackWinnerUnresolved,
	AgentPackConflictPeerRepositoryInstructions:   AgentPackWinnerUnresolved,
	AgentPackConflictRepositoryManifestNotApplied: AgentPackWinnerAgentPack,
	AgentPackConflictHostUserConfigurationLoaded:  AgentPackWinnerUnresolved,
}

type AgentPackCompositionSource struct {
	RepositoryID string                `json:"repository_id,omitempty"`
	Path         string                `json:"path,omitempty"`
	SourceType   AgentConfigSourceType `json:"source_type"`
	DigestSHA256 string                `json:"digest_sha256,omitempty"`
}

type AgentPackCompositionLayer struct {
	Layer   string                       `json:"layer"`
	Status  string                       `json:"status"`
	Sources []AgentPackCompositionSource `json:"sources"`
}

type AgentPackCompositionConflict struct {
	Code         string `json:"code"`
	Layer        string `json:"layer"`
	RepositoryID string `json:"repository_id,omitempty"`
	Path         string `json:"path,omitempty"`
	Winner       string `json:"winner"`
}

type AgentPackCompositionPack struct {
	Version      string `json:"version"`
	Digest       string `json:"digest"`
	KeyID        string `json:"key_id"`
	PluginLoaded bool   `json:"plugin_loaded"`
}

type AgentPackCompositionEvent struct {
	Kind              string                         `json:"kind"`
	ManifestKind      JobKind                        `json:"manifest_kind"`
	LeaseID           string                         `json:"lease_id"`
	JobID             string                         `json:"job_id"`
	AttemptID         string                         `json:"attempt_id"`
	ManifestID        string                         `json:"manifest_id"`
	ManifestDigest    string                         `json:"manifest_digest"`
	FencingToken      uint64                         `json:"fencing_token"`
	ObservedAt        time.Time                      `json:"observed_at"`
	AgentRunAuthority *AgentRunAuthority             `json:"agent_run_authority,omitempty"`
	SchemaVersion     int                            `json:"schema_version"`
	Phase             string                         `json:"phase"`
	Adapter           string                         `json:"adapter"`
	Isolation         string                         `json:"isolation"`
	AgentPack         *AgentPackCompositionPack      `json:"agent_pack"`
	PackView          string                         `json:"pack_view"`
	Layers            []AgentPackCompositionLayer    `json:"layers"`
	Conflicts         []AgentPackCompositionConflict `json:"conflicts"`
	Truncated         bool                           `json:"truncated"`
}
