package daemon

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func (d *Daemon) executeDecisionResponse(command v1.CommandPayload) error {
	if err := validation.ID("job_id", command.JobID); err != nil {
		return fmt.Errorf("decision_response: %w", err)
	}
	if err := validation.ID("attempt_id", command.AttemptID); err != nil {
		return fmt.Errorf("decision_response: %w", err)
	}
	if command.FencingToken == 0 {
		return fmt.Errorf("decision_response: fencing_token must be non-zero")
	}

	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	job := d.activeJob
	if job == nil {
		return fmt.Errorf("no active job to answer")
	}
	if command.JobID != job.job.JobID {
		return fmt.Errorf("decision_response: job %s is not active", command.JobID)
	}
	if command.AttemptID != job.job.AttemptID {
		return fmt.Errorf("decision_response: attempt %s is not active", command.AttemptID)
	}
	if command.FencingToken != job.job.FencingToken {
		return fmt.Errorf("decision_response: stale fencing token %d", command.FencingToken)
	}
	if err := d.leases.Validate(command.AttemptID, command.FencingToken); err != nil {
		return fmt.Errorf("decision_response: %w", err)
	}
	return job.deliverDecision(command)
}
