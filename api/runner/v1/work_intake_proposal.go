package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	WorkIntakeProposalSchemaVersion    = 1
	WorkIntakeProposalTransportVersion = 1
)

type WorkIntakeProposalKind string

const (
	WorkIntakeProposalQuestions WorkIntakeProposalKind = "questions"
	WorkIntakeProposalDraft     WorkIntakeProposalKind = "draft"
)

type WorkIntakeProposalIntent struct {
	Problem        string `json:"problem"`
	AffectedUser   string `json:"affected_user"`
	DesiredOutcome string `json:"desired_outcome"`
}

type WorkIntakeProposalClarification struct {
	SourceQuestionIndex int    `json:"source_question_index"`
	Question            string `json:"question"`
	Answer              string `json:"answer"`
}

// WorkIntakeProposalAuthority binds one delivery attempt to the runner-local
// consultation configuration. It deliberately contains no issue-provider,
// repository, workflow, agent-run, file, URL, or raw model authority.
type WorkIntakeProposalAuthority struct {
	SchemaVersion               int       `json:"schema_version"`
	WorkIntakeProposalRequestID string    `json:"work_intake_proposal_request_id"`
	WorkIntakeProposalRevision  int       `json:"work_intake_proposal_revision"`
	WorkIntakeProposalEffectID  string    `json:"work_intake_proposal_effect_id"`
	WorkIntakeProposalAttemptID string    `json:"work_intake_proposal_attempt_id"`
	ProjectID                   string    `json:"project_id"`
	InputDigest                 string    `json:"input_digest"`
	Adapter                     string    `json:"adapter"`
	Model                       string    `json:"model"`
	PromptTemplateDigest        string    `json:"prompt_template_digest"`
	PolicyDigest                string    `json:"policy_digest"`
	ModelExecutionConfigDigest  string    `json:"model_execution_config_digest"`
	LeaseID                     string    `json:"lease_id"`
	FencingToken                uint64    `json:"fencing_token"`
	LeaseExpiresAt              time.Time `json:"lease_expires_at"`
	CommandDigest               string    `json:"command_digest"`
}

type WorkIntakeProposalOperation struct {
	WorkIntakeProposalAuthority
	Kind           WorkIntakeProposalKind            `json:"kind"`
	Intent         WorkIntakeProposalIntent          `json:"intent"`
	Clarifications []WorkIntakeProposalClarification `json:"clarifications"`
}

type WorkIntakeProposalDraftFields struct {
	Problem            string   `json:"problem"`
	AffectedUser       string   `json:"affected_user"`
	DesiredOutcome     string   `json:"desired_outcome"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	NonGoals           []string `json:"non_goals"`
	Risk               string   `json:"risk"`
	WorkType           string   `json:"work_type"`
}

type WorkIntakeProposal struct {
	SchemaVersion int                            `json:"schema_version"`
	Kind          WorkIntakeProposalKind         `json:"kind"`
	Questions     []string                       `json:"-"`
	Draft         *WorkIntakeProposalDraftFields `json:"draft,omitempty"`
}

func (proposal WorkIntakeProposal) MarshalJSON() ([]byte, error) {
	switch proposal.Kind {
	case WorkIntakeProposalQuestions:
		return json.Marshal(struct {
			SchemaVersion int                    `json:"schema_version"`
			Kind          WorkIntakeProposalKind `json:"kind"`
			Questions     []string               `json:"questions"`
		}{proposal.SchemaVersion, proposal.Kind, proposal.Questions})
	case WorkIntakeProposalDraft:
		return json.Marshal(struct {
			SchemaVersion int                            `json:"schema_version"`
			Kind          WorkIntakeProposalKind         `json:"kind"`
			Draft         *WorkIntakeProposalDraftFields `json:"draft"`
		}{proposal.SchemaVersion, proposal.Kind, proposal.Draft})
	default:
		return nil, fmt.Errorf("unsupported work intake proposal kind %q", proposal.Kind)
	}
}

func (proposal *WorkIntakeProposal) UnmarshalJSON(data []byte) error {
	var discriminator struct {
		Kind WorkIntakeProposalKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return fmt.Errorf("decode work intake proposal kind: %w", err)
	}
	switch discriminator.Kind {
	case WorkIntakeProposalQuestions:
		var wire struct {
			SchemaVersion int                    `json:"schema_version"`
			Kind          WorkIntakeProposalKind `json:"kind"`
			Questions     []string               `json:"questions"`
		}
		if err := decodeExactWorkIntakeProposal(data, &wire); err != nil {
			return err
		}
		*proposal = WorkIntakeProposal{SchemaVersion: wire.SchemaVersion, Kind: wire.Kind, Questions: append([]string(nil), wire.Questions...)}
		if wire.Questions != nil && proposal.Questions == nil {
			proposal.Questions = []string{}
		}
		return nil
	case WorkIntakeProposalDraft:
		var wire struct {
			SchemaVersion int                            `json:"schema_version"`
			Kind          WorkIntakeProposalKind         `json:"kind"`
			Draft         *WorkIntakeProposalDraftFields `json:"draft"`
		}
		if err := decodeExactWorkIntakeProposal(data, &wire); err != nil {
			return err
		}
		*proposal = WorkIntakeProposal{SchemaVersion: wire.SchemaVersion, Kind: wire.Kind, Draft: wire.Draft}
		return nil
	default:
		return fmt.Errorf("unsupported work intake proposal kind %q", discriminator.Kind)
	}
}

func decodeExactWorkIntakeProposal(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode work intake proposal: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode work intake proposal: trailing JSON")
	}
	return nil
}

type WorkIntakeProposalOutcomeKind string

const (
	WorkIntakeProposalSucceeded        WorkIntakeProposalOutcomeKind = "succeeded"
	WorkIntakeProposalRetryableFailure WorkIntakeProposalOutcomeKind = "retryable_failure"
	WorkIntakeProposalPermanentFailure WorkIntakeProposalOutcomeKind = "permanent_failure"
	WorkIntakeProposalUncertain        WorkIntakeProposalOutcomeKind = "uncertain"
)

type WorkIntakeProposalErrorCode string

const (
	WorkIntakeProposalTemporarilyUnavailable WorkIntakeProposalErrorCode = "temporarily_unavailable"
	WorkIntakeProposalProviderUnavailable    WorkIntakeProposalErrorCode = "provider_unavailable"
	WorkIntakeProposalInvalidOutput          WorkIntakeProposalErrorCode = "invalid_output"
	WorkIntakeProposalGenerationUncertain    WorkIntakeProposalErrorCode = "generation_uncertain"
)

type WorkIntakeProposalOutcome struct {
	Kind     WorkIntakeProposalOutcomeKind `json:"kind"`
	Proposal *WorkIntakeProposal           `json:"proposal,omitempty"`
	Code     WorkIntakeProposalErrorCode   `json:"code,omitempty"`
}

const EventWorkIntakeProposalResult = "work_intake_proposal_result"

type WorkIntakeProposalResultEvent struct {
	Kind string `json:"kind"`
	WorkIntakeProposalAuthority
	CommandMessageID string                    `json:"command_message_id"`
	ObservedAt       time.Time                 `json:"observed_at"`
	Outcome          WorkIntakeProposalOutcome `json:"outcome"`
}
