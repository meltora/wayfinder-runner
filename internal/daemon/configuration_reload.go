package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/remoteconfig"
	"github.com/example/app-runner/internal/sourcepublication"
)

const (
	configurationReloadInterval   = 60 * time.Second
	configurationReloadJitter     = 10 * time.Second
	configurationReloadMaxBackoff = 10 * time.Minute
)

type ConfigurationReloader interface {
	Check(ctx context.Context) (remoteconfig.Reload, error)
	Commit(reload remoteconfig.Reload) error
	Refuse(reload remoteconfig.Reload)
}

type hotConfiguration struct {
	repositories                   map[string]config.RepositoryConfig
	agentPackPublicKeys            map[string]string
	sourcePublicationInstallations map[string]config.SourcePublicationInstallationConfig
	labels                         map[string]string
	revokedAgentPackDigests        []string
	digest                         string
}

type sourcePublicationServices struct {
	publisher                            sourcePublicationPublisher
	reviewerIdentityVerifier             reviewerIdentityVerifier
	providerReviewerAssigner             providerReviewerAssigner
	providerReviewerAssignmentReconciler providerReviewerAssignmentReconciler
	providerReviewObserver               providerReviewObserver
	providerReviewFeedbackContentReader  providerReviewFeedbackContentReader
	providerPullRequestRunner            providerPullRequestRunner
}

func (d *Daemon) SetConfigurationReloader(reloader ConfigurationReloader) {
	d.configurationReloader = reloader
}

func (d *Daemon) hotConfiguration() hotConfiguration {
	d.hotMu.RLock()
	defer d.hotMu.RUnlock()
	return hotConfiguration{
		repositories:                   d.cfg.Repositories,
		agentPackPublicKeys:            d.cfg.AgentPackPublicKeys,
		sourcePublicationInstallations: d.cfg.SourcePublicationInstallations,
		labels:                         d.cfg.Labels,
		revokedAgentPackDigests:        d.cfg.RevokedAgentPackDigests,
		digest:                         d.cfg.RunnerConfigurationDigest,
	}
}

func (d *Daemon) sourcePublicationServices() sourcePublicationServices {
	d.hotMu.RLock()
	defer d.hotMu.RUnlock()
	return d.sourcePublicationServicesLocked()
}

func (d *Daemon) sourcePublicationServicesLocked() sourcePublicationServices {
	return sourcePublicationServices{
		publisher:                            d.sourcePublicationPublisher,
		reviewerIdentityVerifier:             d.reviewerIdentityVerifier,
		providerReviewerAssigner:             d.providerReviewerAssigner,
		providerReviewerAssignmentReconciler: d.providerReviewerAssignmentReconciler,
		providerReviewObserver:               d.providerReviewObserver,
		providerReviewFeedbackContentReader:  d.providerReviewFeedbackContentReader,
		providerPullRequestRunner:            d.providerPullRequestRunner,
	}
}

func (jr *jobRunner) jobConfiguration() hotConfiguration {
	if jr.configuration != nil {
		return *jr.configuration
	}
	return jr.d.hotConfiguration()
}

func restartRequiredSettingsEqual(running, next config.Config) bool {
	return running.Isolation == next.Isolation &&
		running.AllowUnsafeDirect == next.AllowUnsafeDirect &&
		running.Provider == next.Provider &&
		running.JobImage == next.JobImage &&
		running.JobNetwork == next.JobNetwork &&
		running.JobNetworkName == next.JobNetworkName &&
		running.EgressPolicyID == next.EgressPolicyID &&
		running.JobMemoryMB == next.JobMemoryMB &&
		running.JobCPUs == next.JobCPUs &&
		running.JobPIDs == next.JobPIDs &&
		running.ControlPlaneURL == next.ControlPlaneURL &&
		running.StateDir == next.StateDir &&
		reflect.DeepEqual(running.ProviderBinaries, next.ProviderBinaries) &&
		reflect.DeepEqual(running.ProviderEnvironment, next.ProviderEnvironment)
}

func (d *Daemon) applyHotConfiguration(next config.Config, digest string) error {
	if !restartRequiredSettingsEqual(d.cfg, next) {
		return errors.New("the new runner configuration changes settings that need a restart")
	}
	current := d.hotConfiguration()
	rebuild := !reflect.DeepEqual(current.sourcePublicationInstallations, next.SourcePublicationInstallations)
	var services sourcePublicationServices
	if rebuild && len(next.SourcePublicationInstallations) != 0 {
		registry, err := sourcepublication.NewRegistry(next.SourcePublicationInstallations, d.sourcePublicationHTTPClient)
		if err != nil {
			return fmt.Errorf("initialize source publication credentials: %w", err)
		}
		services = sourcePublicationServices{
			publisher: registry, reviewerIdentityVerifier: registry, providerReviewerAssigner: registry,
			providerReviewerAssignmentReconciler: registry, providerReviewObserver: registry, providerReviewFeedbackContentReader: registry,
			providerPullRequestRunner: registry,
		}
	}
	d.hotMu.Lock()
	defer d.hotMu.Unlock()
	d.cfg.Repositories = next.Repositories
	d.cfg.AgentPackPublicKeys = next.AgentPackPublicKeys
	d.cfg.SourcePublicationInstallations = next.SourcePublicationInstallations
	d.cfg.Labels = next.Labels
	d.cfg.RevokedAgentPackDigests = next.RevokedAgentPackDigests
	d.cfg.RunnerConfigurationDigest = digest
	if rebuild {
		d.sourcePublicationPublisher = services.publisher
		d.reviewerIdentityVerifier = services.reviewerIdentityVerifier
		d.providerReviewerAssigner = services.providerReviewerAssigner
		d.providerReviewerAssignmentReconciler = services.providerReviewerAssignmentReconciler
		d.providerReviewObserver = services.providerReviewObserver
		d.providerReviewFeedbackContentReader = services.providerReviewFeedbackContentReader
		d.providerPullRequestRunner = services.providerPullRequestRunner
	}
	return nil
}

func (d *Daemon) reloadConfiguration(ctx context.Context) bool {
	if len(d.pendingAgentPackEvictions) != 0 {
		d.pendingAgentPackEvictions = d.evictRevokedAgentPacks(d.pendingAgentPackEvictions, true)
	}
	reload, err := d.configurationReloader.Check(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		if !d.configurationReloadFailing {
			d.logger.Warn("runner configuration refresh failed; the running configuration stays in use", "err", err)
		} else {
			d.logger.Debug("runner configuration refresh failed again", "err", err)
		}
		d.configurationReloadFailing = true
		return false
	}
	if d.configurationReloadFailing {
		d.logger.Info("runner configuration refresh recovered")
		d.configurationReloadFailing = false
	}
	switch reload.Outcome {
	case remoteconfig.ReloadRestartRequired:
		if reload.Delivery.Digest != d.configurationRestartDigest {
			d.logger.Warn("runner configuration needs a restart; restart our-runner to apply it", "changed", reload.RestartKeys, "digest", reload.Delivery.Digest)
			d.configurationRestartDigest = reload.Delivery.Digest
		}
		return true
	case remoteconfig.ReloadApply:
	default:
		return true
	}
	if err := d.applyHotConfiguration(reload.Merged.Config, reload.Delivery.Digest); err != nil {
		d.configurationReloader.Refuse(reload)
		if !d.configurationApplyFailing {
			d.logger.Error("apply runner configuration", "err", err)
		} else {
			d.logger.Debug("apply runner configuration failed again", "err", err)
		}
		d.configurationApplyFailing = true
		return false
	}
	d.configurationApplyFailing = false
	d.configurationRestartDigest = ""
	d.applyAgentPackRevocations()
	d.applyConfiguredAgentPackTrust(ctx, reload.Merged.Config.AgentPackTrustedKeySet)
	if err := d.configurationReloader.Commit(reload); err != nil {
		d.logger.Warn("cache the applied runner configuration", "err", err)
	}
	hot := d.hotConfiguration()
	d.logger.Info("applied runner configuration without a restart", "digest", hot.digest,
		"repositories", len(hot.repositories), "agent_pack_keys", len(hot.agentPackPublicKeys))
	if d.controlReady.Load() {
		if err := d.publishCapability(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("publish capability manifest after configuration change", "err", err)
		}
	}
	return true
}

func configurationReloadDelay(failures int, jitter float64) time.Duration {
	delay := configurationReloadInterval
	for step := 1; step < failures && delay < configurationReloadMaxBackoff; step++ {
		delay *= 2
	}
	delay = min(delay, configurationReloadMaxBackoff)
	return delay + time.Duration((jitter*2-1)*float64(configurationReloadJitter))
}

func (d *Daemon) configurationReloadLoop(ctx context.Context) {
	wait := d.configurationReloadWait
	if wait == nil {
		wait = func(failures int) time.Duration {
			return configurationReloadDelay(failures, rand.Float64())
		}
	}
	failures := 0
	for {
		timer := time.NewTimer(wait(failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if d.reloadConfiguration(ctx) {
			failures = 0
		} else {
			failures++
		}
	}
}

func (d *Daemon) applyAgentPackRevocations() {
	d.pendingAgentPackEvictions = d.evictRevokedAgentPacks(d.hotConfiguration().revokedAgentPackDigests, false)
}

func (d *Daemon) evictRevokedAgentPacks(digests []string, retry bool) []string {
	if len(digests) == 0 {
		return nil
	}
	evicted, skipped, err := d.agentPackArchiveCache().Evict(digests)
	if err != nil {
		d.logger.Warn("could not evict every revoked Agent Pack archive; retrying on the next configuration check",
			"evicted", len(evicted), "skipped", len(skipped), "evicted_digests", evicted, "skipped_digests", skipped)
		return digests
	}
	if len(evicted) != 0 || (len(skipped) != 0 && !retry) {
		d.logger.Info("evicted revoked Agent Pack archives",
			"evicted", len(evicted), "skipped", len(skipped), "evicted_digests", evicted, "skipped_digests", skipped)
	}
	return skipped
}

func (d *Daemon) agentEnvironment(provider string) []string {
	d.hotMu.RLock()
	defer d.hotMu.RUnlock()
	return d.cfg.AgentEnvironment(provider)
}
