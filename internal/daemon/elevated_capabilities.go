package daemon

import (
	"context"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/elevatedcapability"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/validation"
)

func (jr *jobRunner) elevatedProtectedDirs(container bool) []string {
	var protectedDirs []string
	if jr.d != nil && jr.d.cfg.StateDir != "" {
		protectedDirs = append(protectedDirs, jr.d.cfg.StateDir)
	}
	if jr.workDir != "" {
		protectedDirs = append(protectedDirs, jr.workDir)
	}
	if container {
		protectedDirs = append(protectedDirs, isolation.ContainerHome)
	}
	return protectedDirs
}

func (jr *jobRunner) resolveElevatedCapabilities(adapter string, readOnly, container bool) elevatedcapability.Grants {
	repositories := map[string]string{}
	for _, repository := range jr.repositories {
		repositories[repository.ID] = repository.WorkDir
	}
	resolution := elevatedcapability.Resolve(elevatedcapability.Input{
		Grants: jr.job.ElevatedCapabilities, Repositories: repositories, ProtectedDirs: jr.elevatedProtectedDirs(container),
		Adapter: adapter, ReadOnly: readOnly, ContainerIsolation: container, Now: time.Now().UTC(),
	})
	jr.elevatedOutcomes = resolution.Outcomes
	for _, outcome := range resolution.Outcomes {
		jr.logger.Info("elevated capability grant",
			"request_id", outcome.RequestID, "kind", string(outcome.Kind), "applied", outcome.Applied(), "reason", string(outcome.Reason))
	}
	return resolution.Grants
}

func (jr *jobRunner) publishElevatedCapabilityOutcomes(ctx context.Context) {
	if jr.job.ElevatedCapabilities == nil || len(jr.elevatedOutcomes) == 0 || !jr.startEvidenceRecorded() {
		return
	}
	event := v1.ElevatedCapabilityOutcomesEvent{
		Kind: v1.EventKindElevatedCapabilityOutcomes, ManifestKind: jr.job.Kind, LeaseID: jr.job.LeaseID,
		JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, ManifestID: jr.job.ManifestID,
		ManifestDigest: jr.job.ManifestDigest, FencingToken: jr.job.FencingToken, ObservedAt: time.Now().UTC(),
		AgentRunAuthority: jr.job.AgentRunAuthority, SchemaVersion: v1.ElevatedCapabilityOutcomesSchemaVersion,
	}
	for _, outcome := range jr.elevatedOutcomes {
		event.Outcomes = append(event.Outcomes, v1.ElevatedCapabilityOutcome{
			RequestID: outcome.RequestID, Applied: outcome.Applied(), Reason: string(outcome.Reason),
		})
	}
	event.ContentDigestSHA256 = v1.ElevatedCapabilityOutcomesContentDigest(event)
	if err := validation.ElevatedCapabilityOutcomesEvent(event); err != nil {
		jr.logger.Warn("reject invalid elevated capability outcomes", "err", err)
		return
	}
	eventID := "elevated-capability-outcomes-" + jobStartedEventID(jr.job)[len("job-started-"):]
	if _, err := jr.d.sendEventWithStableID(ctx, eventID, event); err != nil {
		jr.logger.Warn("publish elevated capability outcomes", "err", err)
	}
}
