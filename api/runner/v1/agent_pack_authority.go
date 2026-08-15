package v1

const (
	AgentPackAuthoritySchemaVersion = 1
	AgentPackScopePlatform          = "platform"
	AgentPackScopeOrganization      = "organization"
)

// AgentPackAuthorityScope identifies the audience covered by a signed Agent
// Pack publication. Organization-scoped packs must never be accepted by a
// runner serving a different organization.
type AgentPackAuthorityScope struct {
	Kind           string `json:"kind"`
	OrganizationID string `json:"organization_id,omitempty"`
}

// SignedAgentPackAuthority carries the immutable publication attestation that
// authorizes an exact archive for an exact provider and runner protocol.
type SignedAgentPackAuthority struct {
	SchemaVersion        int                     `json:"schema_version"`
	RegistryVersionID    string                  `json:"registry_version_id"`
	Scope                AgentPackAuthorityScope `json:"scope"`
	PackKey              string                  `json:"pack_key"`
	PackName             string                  `json:"pack_name"`
	Provider             string                  `json:"provider"`
	ProtocolVersion      int                     `json:"protocol_version"`
	MinimumRunnerVersion string                  `json:"minimum_runner_version"`
	AgentPack            AgentPackRef            `json:"agent_pack"`
	AttestationDigest    string                  `json:"attestation_digest"`
	SignatureBase64      string                  `json:"signature_base64"`
}
