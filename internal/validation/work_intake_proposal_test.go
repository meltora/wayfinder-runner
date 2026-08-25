package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validWorkIntakeProposalOperation() v1.WorkIntakeProposalOperation {
	return v1.WorkIntakeProposalOperation{
		WorkIntakeProposalAuthority: v1.WorkIntakeProposalAuthority{
			SchemaVersion: v1.WorkIntakeProposalSchemaVersion, WorkIntakeProposalRequestID: "request-1",
			WorkIntakeProposalRevision: 1, WorkIntakeProposalEffectID: "effect-1", WorkIntakeProposalAttemptID: "attempt-1",
			ProjectID: "project-1", InputDigest: strings.Repeat("a", 64), Adapter: "codex", Model: "consultation-model",
			PromptTemplateDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), LeaseID: "lease-1",
			ModelExecutionConfigDigest: strings.Repeat("e", 64),
			FencingToken:               1, LeaseExpiresAt: time.Now().Add(time.Hour).UTC(), CommandDigest: strings.Repeat("d", 64),
		},
		Kind: v1.WorkIntakeProposalQuestions,
		Intent: v1.WorkIntakeProposalIntent{
			Problem: "Planning is inconsistent", AffectedUser: "Delivery teams", DesiredOutcome: "A reviewable draft",
		},
		Clarifications: []v1.WorkIntakeProposalClarification{},
	}
}

func validDraftProposal() v1.WorkIntakeProposal {
	return v1.WorkIntakeProposal{
		SchemaVersion: v1.WorkIntakeProposalSchemaVersion,
		Kind:          v1.WorkIntakeProposalDraft,
		Draft: &v1.WorkIntakeProposalDraftFields{
			Problem: "Planning is inconsistent", AffectedUser: "Delivery teams", DesiredOutcome: "A reviewable draft",
			Title: "Make planning consistent", Description: "Create a consistent proposal flow.",
			AcceptanceCriteria: []string{"A proposal can be reviewed"}, NonGoals: []string{"Automatic issuance"},
			Risk: "medium", WorkType: "feature",
		},
	}
}

func TestWorkIntakeProposalOperationEnforcesRevisionAndClarificationStateMachine(t *testing.T) {
	if err := WorkIntakeProposalOperation(validWorkIntakeProposalOperation()); err != nil {
		t.Fatal(err)
	}
	draftRev1 := validWorkIntakeProposalOperation()
	draftRev1.Kind = v1.WorkIntakeProposalDraft
	if err := WorkIntakeProposalOperation(draftRev1); err != nil {
		t.Fatalf("revision 1 draft should be valid: %v", err)
	}
	draftRev2 := validWorkIntakeProposalOperation()
	draftRev2.WorkIntakeProposalRevision = 2
	draftRev2.Kind = v1.WorkIntakeProposalDraft
	draftRev2.Clarifications = []v1.WorkIntakeProposalClarification{{SourceQuestionIndex: 0, Question: "Who?", Answer: "Project leads"}}
	if err := WorkIntakeProposalOperation(draftRev2); err != nil {
		t.Fatalf("revision 2 draft should be valid: %v", err)
	}

	mutations := map[string]func(*v1.WorkIntakeProposalOperation){
		"schema":                  func(v *v1.WorkIntakeProposalOperation) { v.SchemaVersion = 2 },
		"request id":              func(v *v1.WorkIntakeProposalOperation) { v.WorkIntakeProposalRequestID = "" },
		"revision":                func(v *v1.WorkIntakeProposalOperation) { v.WorkIntakeProposalRevision = 3 },
		"effect":                  func(v *v1.WorkIntakeProposalOperation) { v.WorkIntakeProposalEffectID = "" },
		"attempt":                 func(v *v1.WorkIntakeProposalOperation) { v.WorkIntakeProposalAttemptID = "" },
		"project":                 func(v *v1.WorkIntakeProposalOperation) { v.ProjectID = "" },
		"lease":                   func(v *v1.WorkIntakeProposalOperation) { v.LeaseID = "" },
		"fence zero":              func(v *v1.WorkIntakeProposalOperation) { v.FencingToken = 0 },
		"fence unsafe":            func(v *v1.WorkIntakeProposalOperation) { v.FencingToken = 1 << 53 },
		"expiry":                  func(v *v1.WorkIntakeProposalOperation) { v.LeaseExpiresAt = time.Time{} },
		"input digest":            func(v *v1.WorkIntakeProposalOperation) { v.InputDigest = strings.Repeat("A", 64) },
		"adapter":                 func(v *v1.WorkIntakeProposalOperation) { v.Adapter = "" },
		"model":                   func(v *v1.WorkIntakeProposalOperation) { v.Model = "" },
		"template digest":         func(v *v1.WorkIntakeProposalOperation) { v.PromptTemplateDigest = "bad" },
		"policy digest":           func(v *v1.WorkIntakeProposalOperation) { v.PolicyDigest = "bad" },
		"execution config digest": func(v *v1.WorkIntakeProposalOperation) { v.ModelExecutionConfigDigest = "bad" },
		"command digest":          func(v *v1.WorkIntakeProposalOperation) { v.CommandDigest = "bad" },
		"kind":                    func(v *v1.WorkIntakeProposalOperation) { v.Kind = "other" },
		"blank problem":           func(v *v1.WorkIntakeProposalOperation) { v.Intent.Problem = " " },
		"rev1 clarification": func(v *v1.WorkIntakeProposalOperation) {
			v.Clarifications = []v1.WorkIntakeProposalClarification{{Question: "Who?", Answer: "Leads"}}
		},
		"rev2 questions": func(v *v1.WorkIntakeProposalOperation) {
			v.WorkIntakeProposalRevision = 2
			v.Kind = v1.WorkIntakeProposalQuestions
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validWorkIntakeProposalOperation()
			mutate(&candidate)
			if err := WorkIntakeProposalOperation(candidate); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestWorkIntakeProposalClarificationsEnforceCanonicalBounds(t *testing.T) {
	valid := validWorkIntakeProposalOperation()
	valid.WorkIntakeProposalRevision = 2
	valid.Kind = v1.WorkIntakeProposalDraft
	valid.Clarifications = []v1.WorkIntakeProposalClarification{{
		SourceQuestionIndex: 3, Question: strings.Repeat("é", 500), Answer: strings.Repeat("é", 500),
	}}
	if err := WorkIntakeProposalOperation(valid); err != nil {
		t.Fatalf("500 code points / 1000 bytes should pass: %v", err)
	}
	for name, mutate := range map[string]func(*v1.WorkIntakeProposalOperation){
		"too many": func(v *v1.WorkIntakeProposalOperation) {
			v.Clarifications = append(v.Clarifications, v.Clarifications[0], v.Clarifications[0], v.Clarifications[0], v.Clarifications[0])
		},
		"source index":         func(v *v1.WorkIntakeProposalOperation) { v.Clarifications[0].SourceQuestionIndex = 4 },
		"blank question":       func(v *v1.WorkIntakeProposalOperation) { v.Clarifications[0].Question = " " },
		"too many code points": func(v *v1.WorkIntakeProposalOperation) { v.Clarifications[0].Answer = strings.Repeat("a", 501) },
		"control":              func(v *v1.WorkIntakeProposalOperation) { v.Clarifications[0].Answer = "bad\u0000value" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Clarifications = append([]v1.WorkIntakeProposalClarification(nil), valid.Clarifications...)
			mutate(&candidate)
			if err := WorkIntakeProposalOperation(candidate); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestWorkIntakeProposalResultAcceptsOnlyStrictNormalizedUnion(t *testing.T) {
	event := validWorkIntakeProposalResultEvent()
	if err := WorkIntakeProposalResultEvent(event); err != nil {
		t.Fatal(err)
	}
	late := event
	late.ObservedAt = late.LeaseExpiresAt.Add(time.Minute)
	late.Outcome = v1.WorkIntakeProposalOutcome{Kind: v1.WorkIntakeProposalUncertain, Code: v1.WorkIntakeProposalGenerationUncertain}
	if err := WorkIntakeProposalResultEvent(late); err != nil {
		t.Fatalf("actual late observation timestamp should be preserved: %v", err)
	}
	questions := event
	questions.Outcome.Proposal = &v1.WorkIntakeProposal{
		SchemaVersion: v1.WorkIntakeProposalSchemaVersion, Kind: v1.WorkIntakeProposalQuestions,
		Questions: []string{},
	}
	if err := WorkIntakeProposalResultEvent(questions); err != nil {
		t.Fatalf("zero questions is valid: %v", err)
	}
	for _, outcome := range []v1.WorkIntakeProposalOutcome{
		{Kind: v1.WorkIntakeProposalRetryableFailure, Code: v1.WorkIntakeProposalTemporarilyUnavailable},
		{Kind: v1.WorkIntakeProposalRetryableFailure, Code: v1.WorkIntakeProposalProviderUnavailable},
		{Kind: v1.WorkIntakeProposalPermanentFailure, Code: v1.WorkIntakeProposalInvalidOutput},
		{Kind: v1.WorkIntakeProposalUncertain, Code: v1.WorkIntakeProposalGenerationUncertain},
	} {
		candidate := event
		candidate.Outcome = outcome
		if err := WorkIntakeProposalResultEvent(candidate); err != nil {
			t.Fatalf("outcome %+v should be valid: %v", outcome, err)
		}
	}

	mutations := map[string]func(*v1.WorkIntakeProposalResultEvent){
		"event kind": func(v *v1.WorkIntakeProposalResultEvent) { v.Kind = "other" },
		"command":    func(v *v1.WorkIntakeProposalResultEvent) { v.CommandMessageID = "" },
		"completed":  func(v *v1.WorkIntakeProposalResultEvent) { v.ObservedAt = time.Time{} },
		"mixed union": func(v *v1.WorkIntakeProposalResultEvent) {
			v.Outcome.Code = v1.WorkIntakeProposalInvalidOutput
		},
		"five questions": func(v *v1.WorkIntakeProposalResultEvent) {
			v.Outcome.Proposal = &v1.WorkIntakeProposal{SchemaVersion: 1, Kind: v1.WorkIntakeProposalQuestions, Questions: []string{"1", "2", "3", "4", "5"}}
		},
		"missing draft field": func(v *v1.WorkIntakeProposalResultEvent) { v.Outcome.Proposal.Draft.Title = "" },
		"empty acceptance":    func(v *v1.WorkIntakeProposalResultEvent) { v.Outcome.Proposal.Draft.AcceptanceCriteria = nil },
		"wrong failure code": func(v *v1.WorkIntakeProposalResultEvent) {
			v.Outcome = v1.WorkIntakeProposalOutcome{Kind: v1.WorkIntakeProposalPermanentFailure, Code: v1.WorkIntakeProposalProviderUnavailable}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validWorkIntakeProposalResultEvent()
			mutate(&candidate)
			if err := WorkIntakeProposalResultEvent(candidate); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestWorkIntakeProposalTextMatchesMultilineAndUTF16Boundaries(t *testing.T) {
	operation := validWorkIntakeProposalOperation()
	operation.Intent.Problem = "A valid problem\nwith a second line"
	operation.Intent.DesiredOutcome = "A valid outcome\nwith a second line"
	if err := WorkIntakeProposalOperation(operation); err != nil {
		t.Fatalf("canonical LF multiline text rejected: %v", err)
	}
	operation.WorkIntakeProposalRevision = 2
	operation.Kind = v1.WorkIntakeProposalDraft
	operation.Clarifications = []v1.WorkIntakeProposalClarification{{
		SourceQuestionIndex: 0, Question: strings.Repeat("😀", 250), Answer: strings.Repeat("😀", 250),
	}}
	if err := WorkIntakeProposalOperation(operation); err != nil {
		t.Fatalf("500 UTF-16 unit astral boundary rejected: %v", err)
	}
	operation.Clarifications[0].Answer += "a"
	if err := WorkIntakeProposalOperation(operation); err == nil {
		t.Fatal("501 UTF-16 unit astral boundary accepted")
	}
}

func TestCompactProposalJSONMatchesJavaScriptEscaping(t *testing.T) {
	value := v1.WorkIntakeProposal{
		SchemaVersion: 1, Kind: v1.WorkIntakeProposalQuestions,
		Questions: []string{"<>&\u2028\u2029", `literal \u2028`},
	}
	raw, err := compactWorkIntakeProposalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"schema_version\":1,\"kind\":\"questions\",\"questions\":[\"<>&\u2028\u2029\",\"literal \\\\u2028\"]}"
	if string(raw) != want {
		t.Fatalf("compact JSON\n got: %q\nwant: %q", raw, want)
	}
}

func validWorkIntakeProposalResultEvent() v1.WorkIntakeProposalResultEvent {
	operation := validWorkIntakeProposalOperation()
	return v1.WorkIntakeProposalResultEvent{
		Kind: v1.EventWorkIntakeProposalResult, WorkIntakeProposalAuthority: operation.WorkIntakeProposalAuthority,
		CommandMessageID: "command-1", ObservedAt: time.Now().UTC(),
		Outcome: v1.WorkIntakeProposalOutcome{Kind: v1.WorkIntakeProposalSucceeded, Proposal: func() *v1.WorkIntakeProposal { value := validDraftProposal(); return &value }()},
	}
}
