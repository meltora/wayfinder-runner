package remoteconfig

import (
	"errors"
	"fmt"

	"github.com/example/app-runner/internal/config"
)

var ErrDirectNotAcknowledged = errors.New("the dashboard selected direct isolation, which runs jobs on this host without a sandbox; run our-runner configure and confirm it, or pass --acknowledge-direct")

type Merged struct {
	Config           config.Config
	AcceptedPackKeys []PackKey
	AwaitingPackKeys []PackKey
}

func Merge(local Local, delivery Delivery) (Merged, error) {
	var out Merged
	if err := local.Validate(); err != nil {
		return out, err
	}
	remote := delivery.Remote
	if remote.Isolation == "direct" && !local.AcknowledgeDirect {
		return out, ErrDirectNotAcknowledged
	}
	cfg := config.Default()
	cfg.ControlPlaneURL = local.ControlPlaneURL
	cfg.StateDir = local.StateDir
	cfg.ProviderEnvironment = local.ProviderEnvironment
	cfg.ProviderBinaries = local.ProviderBinaries
	cfg.Isolation = remote.Isolation
	cfg.AllowUnsafeDirect = remote.Isolation == "direct"
	cfg.Provider = remote.Provider
	cfg.JobImage = remote.JobImage
	if remote.JobNetwork != "" {
		cfg.JobNetwork = remote.JobNetwork
	}
	if remote.JobMemoryMB != 0 {
		cfg.JobMemoryMB = remote.JobMemoryMB
	}
	if remote.JobCPUs != 0 {
		cfg.JobCPUs = remote.JobCPUs
	}
	if remote.JobPIDs != 0 {
		cfg.JobPIDs = remote.JobPIDs
	}
	cfg.Labels = remote.Labels
	cfg.Repositories = make(map[string]config.RepositoryConfig, len(remote.Repositories))
	for id, url := range remote.Repositories {
		cfg.Repositories[id] = config.RepositoryConfig{URL: url}
	}
	cfg.SourcePublicationInstallations = make(map[string]config.SourcePublicationInstallationConfig, len(remote.SourcePublicationInstallations))
	for id, provider := range remote.SourcePublicationInstallations {
		cfg.SourcePublicationInstallations[id] = config.SourcePublicationInstallationConfig{Provider: provider}
	}
	cfg.RevokedAgentPackDigests = remote.RevokedAgentPackDigests
	accepted := make(map[string]bool, len(local.AcceptedPackKeyFingerprints))
	for _, fingerprint := range local.AcceptedPackKeyFingerprints {
		accepted[fingerprint] = true
	}
	cfg.AgentPackPublicKeys = map[string]string{}
	for _, key := range delivery.PackKeys {
		if accepted[key.Fingerprint] {
			cfg.AgentPackPublicKeys[key.ID] = key.PublicKey
			out.AcceptedPackKeys = append(out.AcceptedPackKeys, key)
		} else {
			out.AwaitingPackKeys = append(out.AwaitingPackKeys, key)
		}
	}
	cfg = config.Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return out, fmt.Errorf("merged runner configuration is invalid: %w", err)
	}
	out.Config = cfg
	return out, nil
}
