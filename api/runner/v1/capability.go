package v1

import "time"

const AgentPackDeliveryAuthorityVersion = 1

type CapabilityManifest struct {
	RunnerID                          string            `json:"runner_id"`
	CollectedAt                       time.Time         `json:"collected_at"`
	OS                                string            `json:"os"`
	Arch                              string            `json:"arch"`
	CPUs                              int               `json:"cpus"`
	MemoryBytes                       uint64            `json:"memory_bytes"`
	DiskFreeBytes                     uint64            `json:"disk_free_bytes"`
	GitVersion                        string            `json:"git_version,omitempty"`
	ContainerEngine                   string            `json:"container_engine,omitempty"`
	ContainerRootless                 bool              `json:"container_rootless"`
	KVM                               bool              `json:"kvm"`
	Agents                            []AgentCapability `json:"agents,omitempty"`
	Supervisors                       []string          `json:"supervisors,omitempty"`
	Labels                            map[string]string `json:"labels,omitempty"`
	RunnerVersion                     string            `json:"runner_version"`
	ProtocolVersion                   int               `json:"protocol_version"`
	AgentPackDeliveryAuthorityVersion int               `json:"agent_pack_delivery_authority_version"`
	TrustedAgentPackKeyIDs            []string          `json:"trusted_agent_pack_key_ids"`
	Signature                         string            `json:"signature"`
}

type AgentCapability struct {
	Provider  string `json:"provider"`
	Version   string `json:"version,omitempty"`
	AuthReady bool   `json:"auth_ready"`
	Available bool   `json:"available"`
}
