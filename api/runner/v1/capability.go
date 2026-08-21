package v1

import "time"

const AgentPackDeliveryAuthorityVersion = 1

type CapabilityManifest struct {
	RunnerID                                     string                                               `json:"runner_id"`
	CollectedAt                                  time.Time                                            `json:"collected_at"`
	OS                                           string                                               `json:"os"`
	Arch                                         string                                               `json:"arch"`
	CPUs                                         int                                                  `json:"cpus"`
	MemoryBytes                                  uint64                                               `json:"memory_bytes"`
	DiskFreeBytes                                uint64                                               `json:"disk_free_bytes"`
	GitVersion                                   string                                               `json:"git_version,omitempty"`
	ContainerEngine                              string                                               `json:"container_engine,omitempty"`
	ContainerRootless                            bool                                                 `json:"container_rootless"`
	KVM                                          bool                                                 `json:"kvm"`
	Agents                                       []AgentCapability                                    `json:"agents,omitempty"`
	Supervisors                                  []string                                             `json:"supervisors,omitempty"`
	Labels                                       map[string]string                                    `json:"labels,omitempty"`
	RunnerVersion                                string                                               `json:"runner_version"`
	ProtocolVersion                              int                                                  `json:"protocol_version"`
	AgentPackDeliveryAuthorityVersion            int                                                  `json:"agent_pack_delivery_authority_version"`
	TrustedAgentPackKeyIDs                       []string                                             `json:"trusted_agent_pack_key_ids"`
	SourcePublicationTransportVersion            int                                                  `json:"source_publication_transport_version,omitempty"`
	SourcePublicationProviders                   []string                                             `json:"source_publication_providers,omitempty"`
	PreviewObservationTransportVersion           int                                                  `json:"preview_observation_transport_version,omitempty"`
	PreviewObservationProfiles                   []PreviewObservationProfileCapability                `json:"preview_observation_profiles,omitempty"`
	ReviewerIdentityVerificationTransportVersion int                                                  `json:"reviewer_identity_verification_transport_version,omitempty"`
	ReviewerIdentityVerificationProviders        []string                                             `json:"reviewer_identity_verification_providers,omitempty"`
	ReviewerIdentityVerificationInstallations    []ReviewerIdentityVerificationInstallationCapability `json:"reviewer_identity_verification_installations,omitempty"`
	ProviderReviewerAssignmentTransportVersion   int                                                  `json:"provider_reviewer_assignment_transport_version,omitempty"`
	ProviderReviewerAssignmentProviders          []string                                             `json:"provider_reviewer_assignment_providers,omitempty"`
	ProviderReviewerAssignmentInstallations      []ProviderReviewerAssignmentInstallationCapability   `json:"provider_reviewer_assignment_installations,omitempty"`
	Signature                                    string                                               `json:"signature"`
}

type ReviewerIdentityVerificationInstallationCapability struct {
	InstallationID      string `json:"installation_id"`
	Provider            string `json:"provider"`
	ProviderInstanceKey string `json:"provider_instance_key"`
}

type ProviderReviewerAssignmentInstallationCapability struct {
	InstallationID      string `json:"installation_id"`
	Provider            string `json:"provider"`
	ProviderInstanceKey string `json:"provider_instance_key"`
}

type AgentCapability struct {
	Provider              string   `json:"provider"`
	Version               string   `json:"version,omitempty"`
	AuthReady             bool     `json:"auth_ready"`
	Available             bool     `json:"available"`
	ManagedHooksSupported bool     `json:"managed_hooks_supported"`
	ManagedHooksVersion   string   `json:"managed_hooks_version,omitempty"`
	ManagedHookEvents     []string `json:"managed_hook_events,omitempty"`
}
