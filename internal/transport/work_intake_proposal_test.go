package transport

import (
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestWorkIntakeProposalDecodeRejectsUnknownAuthoritySmuggling(t *testing.T) {
	base := map[string]any{
		"schema_version": 1, "work_intake_proposal_request_id": "request-1",
		"work_intake_proposal_revision": 1, "work_intake_proposal_effect_id": "effect-1",
		"work_intake_proposal_attempt_id": "attempt-1", "project_id": "project-1",
		"input_digest": stringOf('a', 64), "adapter": "codex", "model": "fixed-model",
		"prompt_template_digest": stringOf('b', 64), "policy_digest": stringOf('c', 64),
		"lease_id": "lease-1", "fencing_token": 1, "lease_expires_at": "2030-08-25T12:01:00Z",
		"command_digest": stringOf('d', 64), "kind": "questions",
		"intent": map[string]any{
			"problem": "Planning is inconsistent", "affected_user": "Delivery teams", "desired_outcome": "A reviewable draft",
		},
		"clarifications": []any{},
	}
	for _, field := range []string{"issue_type_id", "jira_project", "provider", "context_references", "files", "url", "repository_id", "workflow_run_id", "agent_run_id", "mcp", "tools", "pack", "raw_prompt"} {
		t.Run(field, func(t *testing.T) {
			operation := make(map[string]any, len(base)+1)
			for key, value := range base {
				operation[key] = value
			}
			operation[field] = "smuggled"
			envelope := v1.Envelope{Payload: map[string]any{
				"command": v1.CmdWorkIntakeProposal, "work_intake_proposal": operation,
			}}
			if _, err := decodePayload[v1.CommandPayload](envelope); err == nil {
				t.Fatal("unknown authority field was accepted")
			}
		})
	}
}

func stringOf(value byte, count int) string {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
