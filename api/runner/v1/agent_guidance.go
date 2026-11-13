package v1

const (
	AgentGuidanceTransportVersion = 1
	AgentGuidanceSchemaVersion    = 1
	MaxAgentGuidanceContentBytes  = 16384
	MaxAgentGuidanceLayers        = 2
)

type AgentGuidanceLayerName string

const (
	AgentGuidanceLayerOrganization AgentGuidanceLayerName = "organization"
	AgentGuidanceLayerProject      AgentGuidanceLayerName = "project"
)

type AgentGuidanceLayer struct {
	Layer        AgentGuidanceLayerName `json:"layer"`
	Revision     int                    `json:"revision"`
	DigestSHA256 string                 `json:"digest_sha256"`
	Content      string                 `json:"content"`
}

type AgentGuidance struct {
	SchemaVersion int                  `json:"schema_version"`
	Layers        []AgentGuidanceLayer `json:"layers"`
}
