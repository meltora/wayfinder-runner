package v1

import "time"

const AgentPackDeliveryAuthorityVersion = 1

type CapabilityManifest struct {
	RunnerID                                                 string                                                           `json:"runner_id"`
	CollectedAt                                              time.Time                                                        `json:"collected_at"`
	OS                                                       string                                                           `json:"os"`
	Arch                                                     string                                                           `json:"arch"`
	CPUs                                                     int                                                              `json:"cpus"`
	MemoryBytes                                              uint64                                                           `json:"memory_bytes"`
	DiskFreeBytes                                            uint64                                                           `json:"disk_free_bytes"`
	GitVersion                                               string                                                           `json:"git_version,omitempty"`
	ContainerEngine                                          string                                                           `json:"container_engine,omitempty"`
	ContainerRootless                                        bool                                                             `json:"container_rootless"`
	KVM                                                      bool                                                             `json:"kvm"`
	Agents                                                   []AgentCapability                                                `json:"agents,omitempty"`
	Supervisors                                              []string                                                         `json:"supervisors,omitempty"`
	Labels                                                   map[string]string                                                `json:"labels,omitempty"`
	RunnerVersion                                            string                                                           `json:"runner_version"`
	ProtocolVersion                                          int                                                              `json:"protocol_version"`
	AgentPackDeliveryAuthorityVersion                        int                                                              `json:"agent_pack_delivery_authority_version"`
	TrustedAgentPackKeyIDs                                   []string                                                         `json:"trusted_agent_pack_key_ids"`
	SourcePublicationTransportVersion                        int                                                              `json:"source_publication_transport_version,omitempty"`
	SourcePublicationProviders                               []string                                                         `json:"source_publication_providers,omitempty"`
	PreviewObservationTransportVersion                       int                                                              `json:"preview_observation_transport_version,omitempty"`
	PreviewObservationProfiles                               []PreviewObservationProfileCapability                            `json:"preview_observation_profiles,omitempty"`
	PreviewLifecycleTransportVersion                         int                                                              `json:"preview_lifecycle_transport_version,omitempty"`
	PreviewLifecycleProfiles                                 []PreviewLifecycleProfileCapability                              `json:"preview_lifecycle_profiles,omitempty"`
	ReviewerIdentityVerificationTransportVersion             int                                                              `json:"reviewer_identity_verification_transport_version,omitempty"`
	ReviewerIdentityVerificationProviders                    []string                                                         `json:"reviewer_identity_verification_providers,omitempty"`
	ReviewerIdentityVerificationInstallations                []ReviewerIdentityVerificationInstallationCapability             `json:"reviewer_identity_verification_installations,omitempty"`
	ProviderReviewerAssignmentTransportVersion               int                                                              `json:"provider_reviewer_assignment_transport_version,omitempty"`
	ProviderReviewerAssignmentProviders                      []string                                                         `json:"provider_reviewer_assignment_providers,omitempty"`
	ProviderReviewerAssignmentInstallations                  []ProviderReviewerAssignmentInstallationCapability               `json:"provider_reviewer_assignment_installations,omitempty"`
	ProviderReviewerAssignmentReconciliationTransportVersion int                                                              `json:"provider_reviewer_assignment_reconciliation_transport_version,omitempty"`
	ProviderReviewerAssignmentReconciliationProviders        []string                                                         `json:"provider_reviewer_assignment_reconciliation_providers,omitempty"`
	ProviderReviewerAssignmentReconciliationInstallations    []ProviderReviewerAssignmentReconciliationInstallationCapability `json:"provider_reviewer_assignment_reconciliation_installations,omitempty"`
	ProviderReviewObservationTransportVersion                int                                                              `json:"provider_review_observation_transport_version,omitempty"`
	ProviderReviewObservationProviders                       []string                                                         `json:"provider_review_observation_providers,omitempty"`
	ProviderReviewObservationInstallations                   []ProviderReviewObservationInstallationCapability                `json:"provider_review_observation_installations,omitempty"`
	ProviderReviewFeedbackContentTransportVersion            int                                                              `json:"provider_review_feedback_content_transport_version,omitempty"`
	ProviderReviewFeedbackContentProviders                   []string                                                         `json:"provider_review_feedback_content_providers,omitempty"`
	ProviderReviewFeedbackContentInstallations               []ProviderReviewFeedbackContentInstallationCapability            `json:"provider_review_feedback_content_installations,omitempty"`
	AgentRunTransportVersion                                 int                                                              `json:"agent_run_transport_version,omitempty"`
	Signature                                                string                                                           `json:"signature"`
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

type ProviderReviewerAssignmentReconciliationInstallationCapability struct {
	InstallationID      string `json:"installation_id"`
	Provider            string `json:"provider"`
	ProviderInstanceKey string `json:"provider_instance_key"`
}
type ProviderReviewObservationInstallationCapability struct {
	InstallationID      string `json:"installation_id"`
	Provider            string `json:"provider"`
	ProviderInstanceKey string `json:"provider_instance_key"`
}

type ProviderReviewFeedbackContentInstallationCapability struct {
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
