package daemon

import v1 "github.com/example/app-runner/api/runner/v1"

func (jr *jobRunner) jobContext() map[string]any {
	result := map[string]any{
		"project_id":             jr.job.ProjectID,
		"job_id":                 jr.job.JobID,
		"attempt_id":             jr.job.AttemptID,
		"kind":                   jr.job.Kind,
		"workflow_revision":      jr.job.WorkflowRevision,
		"approved_plan_revision": jr.job.ApprovedPlanRevision,
		"ticket":                 jr.job.Ticket,
		"repositories":           append([]v1.JobRepository(nil), jr.job.Repositories...),
		"capabilities":           append([]string(nil), capabilitiesForJob(jr.job.Kind)...),
		"execution_boundaries": map[string]any{
			"workspace_read_only":     jr.job.Kind == v1.JobKindPlan,
			"merge_allowed":           false,
			"feature_done_allowed":    false,
			"human_approval_required": true,
		},
	}
	if jr.job.PlanRevisionContext != nil {
		result["plan_revision_context"] = jr.job.PlanRevisionContext
	}
	if jr.job.Kind == v1.JobKindBuild && jr.job.ApprovedPlan != nil {
		result["approved_plan"] = jr.job.ApprovedPlan
	}
	if jr.job.AgentRunAuthority != nil {
		authority := *jr.job.AgentRunAuthority
		result["agent_run_authority"] = authority
	}
	if jr.job.AgentGuidance != nil {
		result["agent_guidance"] = *jr.job.AgentGuidance
	}
	if checks := jr.runnableChecks(); checks != nil {
		result["runnable_checks"] = checks
	}
	return result
}
