package remoteconfig

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/example/app-runner/internal/config"
)

var ErrDirectNotAcknowledged = errors.New("the dashboard selected direct isolation, which runs jobs on this host without a sandbox; run our-runner configure and confirm it, or pass --acknowledge-direct")

type Merged struct {
	Config        config.Config
	OmittedChecks []OmittedCheck
}

type OmittedCheck struct {
	ID           string
	RepositoryID string
}

func registrableChecks(local map[string]config.CheckConfig, repositories map[string]string) (map[string]config.CheckConfig, []OmittedCheck, error) {
	if local == nil {
		return nil, nil, nil
	}
	checks := make(map[string]config.CheckConfig, len(local))
	var omitted []OmittedCheck
	for id, check := range local {
		if err := config.ValidateCheck(id, check); err != nil {
			return nil, nil, err
		}
		if _, ok := repositories[check.RepositoryID]; check.RepositoryID != "" && !ok {
			omitted = append(omitted, OmittedCheck{ID: id, RepositoryID: check.RepositoryID})
			continue
		}
		checks[id] = check
	}
	slices.SortFunc(omitted, func(a, b OmittedCheck) int { return strings.Compare(a.ID, b.ID) })
	return checks, omitted, nil
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
	checks, omitted, err := registrableChecks(local.Checks, remote.Repositories)
	if err != nil {
		return out, fmt.Errorf("merged runner configuration is invalid: %w", err)
	}
	cfg.Checks = checks
	cfg.Isolation = remote.Isolation
	cfg.AllowUnsafeDirect = remote.Isolation == "direct"
	cfg.Provider = remote.Provider
	cfg.JobImage = remote.JobImage
	if remote.JobNetwork != "" {
		cfg.JobNetwork = remote.JobNetwork
	}
	if remote.JobNetwork == "restricted" {
		cfg.JobNetworkName = remote.JobNetworkName
		cfg.EgressPolicyID = remote.EgressPolicyID
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
	cfg.AgentPackPublicKeys = nil
	cfg.AgentPackTrustedKeySet = remote.AgentPackTrustedKeySet
	cfg = config.Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return out, fmt.Errorf("merged runner configuration is invalid: %w", err)
	}
	out.Config = cfg
	out.OmittedChecks = omitted
	return out, nil
}
