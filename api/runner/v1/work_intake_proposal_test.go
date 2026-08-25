package v1

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkIntakeProposalCommandHasMinimalExactWireShape(t *testing.T) {
	command := CommandPayload{
		Command: CmdWorkIntakeProposal,
		WorkIntakeProposal: &WorkIntakeProposalOperation{
			WorkIntakeProposalAuthority: WorkIntakeProposalAuthority{
				SchemaVersion:               WorkIntakeProposalSchemaVersion,
				WorkIntakeProposalRequestID: "request-1",
				WorkIntakeProposalRevision:  2,
				WorkIntakeProposalEffectID:  "effect-1",
				WorkIntakeProposalAttemptID: "attempt-1",
				ProjectID:                   "project-1",
				LeaseID:                     "lease-1",
				FencingToken:                7,
				LeaseExpiresAt:              time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
				InputDigest:                 strings.Repeat("a", 64),
				Adapter:                     "codex",
				Model:                       "consultation-model",
				PromptTemplateDigest:        strings.Repeat("b", 64),
				PolicyDigest:                strings.Repeat("c", 64),
				ModelExecutionConfigDigest:  strings.Repeat("e", 64),
				CommandDigest:               strings.Repeat("d", 64),
			},
			Kind: WorkIntakeProposalDraft,
			Intent: WorkIntakeProposalIntent{
				Problem: "People cannot find the current status.", AffectedUser: "Project leads", DesiredOutcome: "A clear status view",
			},
			Clarifications: []WorkIntakeProposalClarification{{
				SourceQuestionIndex: 0, Question: "Which status matters?", Answer: "Delivery status",
			}},
		},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"command":"work_intake_proposal","work_intake_proposal":{"schema_version":1,"work_intake_proposal_request_id":"request-1","work_intake_proposal_revision":2,"work_intake_proposal_effect_id":"effect-1","work_intake_proposal_attempt_id":"attempt-1","project_id":"project-1","input_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","adapter":"codex","model":"consultation-model","prompt_template_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","policy_digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","model_execution_config_digest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","lease_id":"lease-1","fencing_token":7,"lease_expires_at":"2026-08-25T12:00:00Z","command_digest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","kind":"draft","intent":{"problem":"People cannot find the current status.","affected_user":"Project leads","desired_outcome":"A clear status view"},"clarifications":[{"source_question_index":0,"question":"Which status matters?","answer":"Delivery status"}]}}`
	if string(raw) != want {
		t.Fatalf("wire payload mismatch\n got: %s\nwant: %s", raw, want)
	}
	for _, forbidden := range []string{
		"issue_type", "jira", "metadata", "context_reference", "file", "url", "repository",
		"workflow", "agent_run", "manifest", "mcp", "tool", "pack", "raw_prompt", "raw_result",
	} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("wire payload contains forbidden field family %q: %s", forbidden, raw)
		}
	}
}

func TestWorkIntakeProposalResultIsNormalizedAndContainsNoRawProviderData(t *testing.T) {
	event := WorkIntakeProposalResultEvent{
		Kind: EventWorkIntakeProposalResult,
		WorkIntakeProposalAuthority: WorkIntakeProposalAuthority{
			SchemaVersion: WorkIntakeProposalSchemaVersion, WorkIntakeProposalRequestID: "request-1",
			WorkIntakeProposalRevision: 1, WorkIntakeProposalEffectID: "effect-1", WorkIntakeProposalAttemptID: "attempt-1",
			ProjectID: "project-1", InputDigest: strings.Repeat("a", 64), Adapter: "codex", Model: "consultation-model",
			PromptTemplateDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), LeaseID: "lease-1",
			ModelExecutionConfigDigest: strings.Repeat("e", 64),
			FencingToken:               3, LeaseExpiresAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC), CommandDigest: strings.Repeat("d", 64),
		},
		CommandMessageID: "command-1",
		ObservedAt:       time.Date(2026, 8, 25, 11, 59, 0, 0, time.UTC),
		Outcome: WorkIntakeProposalOutcome{
			Kind: WorkIntakeProposalSucceeded,
			Proposal: &WorkIntakeProposal{
				SchemaVersion: WorkIntakeProposalSchemaVersion,
				Kind:          WorkIntakeProposalQuestions,
				Questions:     []string{"Who needs this first?"},
			},
		},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"work_intake_proposal_result"`) ||
		!strings.Contains(string(raw), `"proposal":{"schema_version":1,"kind":"questions","questions":["Who needs this first?"]}`) {
		t.Fatalf("normalized result missing: %s", raw)
	}
	for _, forbidden := range []string{"raw_prompt", "raw_response", "stdout", "stderr", "error_message", "tool", "mcp"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("result contains forbidden provider detail %q: %s", forbidden, raw)
		}
	}
}

func TestCapabilityManifestAdvertisesBoundWorkIntakeProposalConfiguration(t *testing.T) {
	raw, err := json.Marshal(CapabilityManifest{
		WorkIntakeProposalTransportVersion:           WorkIntakeProposalTransportVersion,
		WorkIntakeProposalAdapter:                    "codex",
		WorkIntakeProposalModel:                      "consultation-model",
		WorkIntakeProposalPromptTemplateDigest:       strings.Repeat("b", 64),
		WorkIntakeProposalPolicyDigest:               strings.Repeat("c", 64),
		WorkIntakeProposalModelExecutionConfigDigest: strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"work_intake_proposal_transport_version":1`) {
		t.Fatalf("capability missing transport: %s", raw)
	}
	for _, required := range []string{`"work_intake_proposal_adapter":"codex"`, `"work_intake_proposal_model":"consultation-model"`, `"work_intake_proposal_prompt_template_digest":"` + strings.Repeat("b", 64) + `"`, `"work_intake_proposal_policy_digest":"` + strings.Repeat("c", 64) + `"`, `"work_intake_proposal_model_execution_config_digest":"` + strings.Repeat("e", 64) + `"`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("capability missing binding %q: %s", required, raw)
		}
	}
	for _, forbidden := range []string{"work_intake_proposal_provider", "work_intake_proposal_profile", "work_intake_proposal_installation"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("capability exposes forbidden selector %q: %s", forbidden, raw)
		}
	}
}

func TestWorkIntakeProposalWireBindsModelExecutionConfiguration(t *testing.T) {
	command := CommandPayload{
		Command: CmdWorkIntakeProposal,
		WorkIntakeProposal: &WorkIntakeProposalOperation{
			WorkIntakeProposalAuthority: WorkIntakeProposalAuthority{
				SchemaVersion: WorkIntakeProposalSchemaVersion,
			},
		},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"model_execution_config_digest"`)) {
		t.Fatalf("command authority does not bind effective model execution config: %s", raw)
	}

	manifest, err := json.Marshal(CapabilityManifest{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifest, []byte(`"work_intake_proposal_model_execution_config_digest"`)) {
		t.Fatalf("empty optional capability unexpectedly advertised: %s", manifest)
	}
}

func TestZeroQuestionProposalSerializesRequiredEmptyArrayAndStrictlyRoundTrips(t *testing.T) {
	proposal := WorkIntakeProposal{SchemaVersion: 1, Kind: WorkIntakeProposalQuestions, Questions: []string{}}
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"schema_version":1,"kind":"questions","questions":[]}` {
		t.Fatalf("zero-question wire = %s", raw)
	}
	var decoded WorkIntakeProposal
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Questions == nil || len(decoded.Questions) != 0 || decoded.Draft != nil {
		t.Fatalf("decoded = %+v", decoded)
	}
	if err := json.Unmarshal([]byte(`{"schema_version":1,"kind":"questions","questions":[],"draft":null}`), &decoded); err == nil {
		t.Fatal("cross-variant field smuggling accepted")
	}
}
