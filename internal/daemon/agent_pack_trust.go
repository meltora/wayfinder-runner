package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/agentpack/trustset"
	"github.com/example/app-runner/internal/config"
)

type agentPackTrust struct {
	store                  *trustset.Store
	developmentKeysAllowed bool
}

func openAgentPackTrust(cfg config.Config) (*agentPackTrust, error) {
	channel, err := trustroot.ReleaseChannel(Channel)
	if err != nil {
		return nil, err
	}
	developmentBuild := channel == trustroot.ChannelDevelopment
	if !developmentBuild && len(cfg.AgentPackPublicKeys) != 0 {
		return nil, fmt.Errorf("agent_pack_public_keys is honoured only by development builds; remove it from the runner configuration")
	}
	roots, err := trustroot.Resolve(Channel, os.Getenv(trustroot.OverrideEnvironment))
	if err != nil {
		return nil, err
	}
	store, err := trustset.OpenStore(cfg.StateDir, roots)
	if err != nil {
		return nil, err
	}
	return &agentPackTrust{store: store, developmentKeysAllowed: developmentBuild}, nil
}

func (trust *agentPackTrust) applyConfigured(signed *v1.AgentPackTrustedKeySet) error {
	if signed == nil {
		return nil
	}
	_, _, err := trust.store.Apply(*signed)
	return err
}

func (trust *agentPackTrust) snapshot() trustset.Snapshot {
	if trust == nil {
		return trustset.Snapshot{}
	}
	return trust.store.Snapshot()
}

func (d *Daemon) trustNow() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (d *Daemon) applyConfiguredAgentPackTrust(ctx context.Context, signed *v1.AgentPackTrustedKeySet) {
	if d.agentPackTrust == nil || signed == nil {
		return
	}
	before := d.agentPackTrust.snapshot().HighestSequence
	if err := d.agentPackTrust.applyConfigured(signed); err != nil {
		d.logger.Warn("the delivered platform trust set could not be verified; the current trust stays in use", "code", trustset.CodeOf(err))
		return
	}
	if d.agentPackTrust.snapshot().HighestSequence != before {
		d.agentPackTrustChanged(ctx)
	}
}

func (d *Daemon) agentPackTrustChanged(ctx context.Context) {
	if !d.controlReady.Load() {
		return
	}
	go func() {
		if err := d.publishCapability(context.WithoutCancel(ctx)); err != nil {
			d.logger.Warn("publish capability manifest after a platform trust change", "err", err)
		}
	}()
}

func (d *Daemon) trustedAgentPackKey(job v1.JobSpec, developmentKeys map[string]string) (string, error) {
	trust := d.agentPackTrust
	var jobSequence int64
	if job.AgentPackTrustedKeySet != nil {
		if trust == nil {
			return "", rejectCommand(v1.CommandAckReasonAgentPackTrustUnverifiable, nil)
		}
		verified, outcome, err := trust.store.Apply(*job.AgentPackTrustedKeySet)
		if err != nil {
			return "", rejectCommand(v1.CommandAckReasonAgentPackTrustUnverifiable, err)
		}
		jobSequence = verified.Document.Sequence
		if outcome == trustset.Applied {
			d.agentPackTrustChanged(context.Background())
		}
	}
	keyID := job.AgentPack.KeyID
	snapshot := trust.snapshot()
	if snapshot.Current != nil {
		key, err := snapshot.Current.TrustedKey(keyID, d.trustNow())
		switch {
		case err == nil:
			return key.PublicKeySPKIBase64, nil
		case errors.Is(err, trustset.ErrKeyRevoked):
			return "", rejectCommand(v1.CommandAckReasonAgentPackKeyRevoked, nil)
		case errors.Is(err, trustset.ErrKeyOutsideValidity):
			return "", rejectCommand(v1.CommandAckReasonAgentPackTrustUnverifiable, err)
		}
		if job.AgentPackTrustedKeySet != nil && jobSequence < snapshot.HighestSequence {
			return "", rejectCommand(v1.CommandAckReasonAgentPackTrustUnverifiable, nil)
		}
	} else if snapshot.PersistedSetUnverifiable {
		return "", rejectCommand(v1.CommandAckReasonAgentPackTrustUnverifiable, nil)
	}
	if trust != nil && trust.developmentKeysAllowed {
		if key := developmentKeys[keyID]; key != "" {
			return key, nil
		}
	}
	return "", rejectCommand(v1.CommandAckReasonAgentPackKeyUntrusted, nil)
}

func (d *Daemon) agentPackTrustCapability(developmentKeys map[string]string) ([]string, []string) {
	trust := d.agentPackTrust
	roots := trust.store.Roots().KeyIDs()
	var trusted []string
	if current := trust.snapshot().Current; current != nil {
		trusted = current.TrustedKeyIDs(d.trustNow())
	}
	if trust.developmentKeysAllowed {
		for keyID, key := range developmentKeys {
			if key != "" {
				trusted = append(trusted, keyID)
			}
		}
	}
	return roots, trusted
}
