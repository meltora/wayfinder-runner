package daemon

import (
	"time"

	"github.com/example/app-runner/internal/elevatedcapability"
)

func (jr *jobRunner) resolveElevatedCapabilities(adapter, workDir string, readOnly bool) elevatedcapability.Grants {
	resolution := elevatedcapability.Resolve(elevatedcapability.Input{
		Grants: jr.job.ElevatedCapabilities, WorkDir: workDir, Adapter: adapter, ReadOnly: readOnly, Now: time.Now().UTC(),
	})
	jr.elevatedOutcomes = resolution.Outcomes
	for _, outcome := range resolution.Outcomes {
		jr.logger.Info("elevated capability grant",
			"request_id", outcome.RequestID, "kind", string(outcome.Kind), "applied", outcome.Applied(), "reason", string(outcome.Reason))
	}
	return resolution.Grants
}
