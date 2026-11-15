package v1

import "time"

const (
	ElevatedCapabilityTransportVersion = 2
	MaxElevatedCapabilityGrants        = 64
	MaxElevatedCapabilityKeyBytes      = 300
)

type ElevatedCapabilityKind string

const (
	ElevatedCapabilityHook          ElevatedCapabilityKind = "hook"
	ElevatedCapabilityMCPServer     ElevatedCapabilityKind = "mcp_server"
	ElevatedCapabilityNetworkAccess ElevatedCapabilityKind = "network_access"
)

type ElevatedCapabilityGrant struct {
	RequestID         string                 `json:"request_id"`
	Kind              ElevatedCapabilityKind `json:"kind"`
	Key               string                 `json:"key"`
	DeclarationDigest string                 `json:"declaration_digest"`
	ExpiresAt         time.Time              `json:"expires_at"`
	RepositoryID      string                 `json:"repository_id,omitempty"`
}

type ElevatedCapabilityGrants struct {
	SchemaVersion int                       `json:"schema_version"`
	Grants        []ElevatedCapabilityGrant `json:"grants"`
}
