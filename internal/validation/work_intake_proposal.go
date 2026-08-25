package validation

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"golang.org/x/text/unicode/norm"
)

const (
	maxWorkIntakeProposalQuestionsBytes = 8 << 10
	maxWorkIntakeProposalDraftBytes     = 24 << 10
)

func workIntakeProposalText(field, value string, minimumRunes, maximumRunes, maximumBytes int) error {
	if !utf8.ValidString(value) || value != strings.TrimSpace(value) || !norm.NFC.IsNormalString(value) {
		return fmt.Errorf("%s must be trimmed, valid NFC text", field)
	}
	runes := utf8.RuneCountInString(value)
	utf16Units := 0
	for _, current := range value {
		utf16Units += utf16.RuneLen(current)
	}
	if runes < minimumRunes || runes > maximumRunes || utf16Units > maximumRunes || len(value) > maximumBytes {
		return fmt.Errorf("%s is outside its canonical length bounds", field)
	}
	for _, current := range value {
		if (unicode.IsControl(current) && current != '\n') || current == unicode.ReplacementChar || workIntakeProposalBidiControl(current) {
			return fmt.Errorf("%s contains unsafe Unicode", field)
		}
	}
	return nil
}

func workIntakeProposalBidiControl(value rune) bool {
	return value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		(value >= '\u202a' && value <= '\u202e') || (value >= '\u2066' && value <= '\u2069')
}

func workIntakeProposalAuthority(value v1.WorkIntakeProposalAuthority) error {
	if value.SchemaVersion != v1.WorkIntakeProposalSchemaVersion {
		return fmt.Errorf("work intake proposal schema_version is unsupported")
	}
	for field, identifier := range map[string]string{
		"work_intake_proposal_request_id": value.WorkIntakeProposalRequestID,
		"work_intake_proposal_effect_id":  value.WorkIntakeProposalEffectID,
		"work_intake_proposal_attempt_id": value.WorkIntakeProposalAttemptID,
		"project_id":                      value.ProjectID,
		"lease_id":                        value.LeaseID,
	} {
		if err := ID(field, identifier); err != nil {
			return err
		}
	}
	if value.WorkIntakeProposalRevision != 1 && value.WorkIntakeProposalRevision != 2 {
		return fmt.Errorf("work_intake_proposal_revision is unsupported")
	}
	for field, digest := range map[string]string{
		"input_digest":                  value.InputDigest,
		"prompt_template_digest":        value.PromptTemplateDigest,
		"policy_digest":                 value.PolicyDigest,
		"model_execution_config_digest": value.ModelExecutionConfigDigest,
		"command_digest":                value.CommandDigest,
	} {
		// input_digest and command_digest are authenticated control-plane
		// bindings over server-only authority (including creation target,
		// model-config digest, and runner certificate fingerprint). The runner
		// cannot reconstruct them from the deliberately minimal wire payload;
		// it validates their canonical shape and echoes them without using them
		// to authorize or route provider execution.
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if err := ID("adapter", value.Adapter); err != nil {
		return err
	}
	if err := workIntakeProposalText("model", value.Model, 1, 256, 1024); err != nil {
		return err
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("work intake proposal fencing_token is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("work intake proposal lease_expires_at is required")
	}
	return nil
}

func workIntakeProposalIntent(value v1.WorkIntakeProposalIntent) error {
	if err := workIntakeProposalText("problem", value.Problem, 8, 4000, 8000); err != nil {
		return err
	}
	if err := workIntakeProposalText("affected_user", value.AffectedUser, 2, 1000, 2000); err != nil {
		return err
	}
	return workIntakeProposalText("desired_outcome", value.DesiredOutcome, 8, 2000, 4000)
}

func WorkIntakeProposalOperation(value v1.WorkIntakeProposalOperation) error {
	if err := workIntakeProposalAuthority(value.WorkIntakeProposalAuthority); err != nil {
		return err
	}
	if value.Kind != v1.WorkIntakeProposalQuestions && value.Kind != v1.WorkIntakeProposalDraft {
		return fmt.Errorf("work intake proposal kind is unsupported")
	}
	if value.WorkIntakeProposalRevision == 2 && value.Kind != v1.WorkIntakeProposalDraft {
		return fmt.Errorf("revision 2 must request a draft")
	}
	if value.Clarifications == nil || len(value.Clarifications) > 4 {
		return fmt.Errorf("work intake proposal clarifications are invalid")
	}
	if value.WorkIntakeProposalRevision == 1 && len(value.Clarifications) != 0 {
		return fmt.Errorf("revision 1 cannot contain clarifications")
	}
	if err := workIntakeProposalIntent(value.Intent); err != nil {
		return err
	}
	seenIndexes := make(map[int]struct{}, len(value.Clarifications))
	for _, clarification := range value.Clarifications {
		if clarification.SourceQuestionIndex < 0 || clarification.SourceQuestionIndex > 3 {
			return fmt.Errorf("clarification source_question_index is invalid")
		}
		if _, exists := seenIndexes[clarification.SourceQuestionIndex]; exists {
			return fmt.Errorf("clarification source_question_index is duplicated")
		}
		seenIndexes[clarification.SourceQuestionIndex] = struct{}{}
		if err := workIntakeProposalText("clarification question", clarification.Question, 1, 500, 2000); err != nil {
			return err
		}
		if err := workIntakeProposalText("clarification answer", clarification.Answer, 1, 500, 2000); err != nil {
			return err
		}
	}
	return nil
}

func workIntakeProposalUniqueTextList(field string, values []string, minimum, maximum int) error {
	if len(values) < minimum || len(values) > maximum {
		return fmt.Errorf("%s count is invalid", field)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := workIntakeProposalText(field, value, 1, 500, 2000); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicates", field)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func workIntakeProposalDraft(value v1.WorkIntakeProposalDraftFields) error {
	if err := workIntakeProposalIntent(v1.WorkIntakeProposalIntent{
		Problem: value.Problem, AffectedUser: value.AffectedUser, DesiredOutcome: value.DesiredOutcome,
	}); err != nil {
		return err
	}
	if err := workIntakeProposalText("title", value.Title, 1, 255, 1020); err != nil {
		return err
	}
	if err := workIntakeProposalText("description", value.Description, 1, 8000, 8000); err != nil {
		return err
	}
	if err := workIntakeProposalUniqueTextList("acceptance_criteria", value.AcceptanceCriteria, 1, 10); err != nil {
		return err
	}
	if err := workIntakeProposalUniqueTextList("non_goals", value.NonGoals, 0, 10); err != nil {
		return err
	}
	if value.Risk != "low" && value.Risk != "medium" && value.Risk != "high" && value.Risk != "critical" {
		return fmt.Errorf("risk is unsupported")
	}
	if value.WorkType != "feature" && value.WorkType != "bug" && value.WorkType != "maintenance" &&
		value.WorkType != "incident_follow_up" && value.WorkType != "other" {
		return fmt.Errorf("work_type is unsupported")
	}
	return nil
}

func workIntakeProposal(value v1.WorkIntakeProposal) error {
	if value.SchemaVersion != v1.WorkIntakeProposalSchemaVersion {
		return fmt.Errorf("work intake proposal result schema_version is unsupported")
	}
	switch value.Kind {
	case v1.WorkIntakeProposalQuestions:
		if value.Draft != nil || value.Questions == nil || len(value.Questions) > 4 {
			return fmt.Errorf("questions proposal shape is invalid")
		}
		if err := workIntakeProposalUniqueTextList("question", value.Questions, 0, 4); err != nil {
			return err
		}
		return compactWorkIntakeProposalSize(value, maxWorkIntakeProposalQuestionsBytes)
	case v1.WorkIntakeProposalDraft:
		if value.Draft == nil || len(value.Questions) != 0 {
			return fmt.Errorf("draft proposal shape is invalid")
		}
		if err := workIntakeProposalDraft(*value.Draft); err != nil {
			return err
		}
		return compactWorkIntakeProposalSize(value, maxWorkIntakeProposalDraftBytes)
	default:
		return fmt.Errorf("work intake proposal result kind is unsupported")
	}
}

// WorkIntakeProposal validates a normalized provider result independently of
// delivery authority. Callers still validate the complete result event before
// publishing it.
func WorkIntakeProposal(value v1.WorkIntakeProposal) error {
	return workIntakeProposal(value)
}

func compactWorkIntakeProposalSize(value v1.WorkIntakeProposal, maximum int) error {
	raw, err := compactWorkIntakeProposalJSON(value)
	if err != nil {
		return fmt.Errorf("encode work intake proposal: %w", err)
	}
	if len(raw) > maximum {
		return fmt.Errorf("work intake proposal exceeds compact JSON bound")
	}
	return nil
}

func compactWorkIntakeProposalJSON(value v1.WorkIntakeProposal) ([]byte, error) {
	var wire any
	if value.Kind == v1.WorkIntakeProposalQuestions {
		wire = struct {
			SchemaVersion int                       `json:"schema_version"`
			Kind          v1.WorkIntakeProposalKind `json:"kind"`
			Questions     []string                  `json:"questions"`
		}{value.SchemaVersion, value.Kind, value.Questions}
	} else {
		wire = struct {
			SchemaVersion int                               `json:"schema_version"`
			Kind          v1.WorkIntakeProposalKind         `json:"kind"`
			Draft         *v1.WorkIntakeProposalDraftFields `json:"draft"`
		}{value.SchemaVersion, value.Kind, value.Draft}
	}
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(wire); err != nil {
		return nil, err
	}
	raw := []byte(strings.TrimSuffix(buffer.String(), "\n"))
	return restoreJSONLineSeparators(raw), nil
}

func restoreJSONLineSeparators(raw []byte) []byte {
	result := make([]byte, 0, len(raw))
	for index := 0; index < len(raw); {
		if index+6 <= len(raw) && raw[index] == '\\' &&
			(string(raw[index:index+6]) == `\u2028` || string(raw[index:index+6]) == `\u2029`) {
			precedingSlashes := 0
			for cursor := index - 1; cursor >= 0 && raw[cursor] == '\\'; cursor-- {
				precedingSlashes++
			}
			if precedingSlashes%2 == 0 {
				if raw[index+5] == '8' {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				index += 6
				continue
			}
		}
		result = append(result, raw[index])
		index++
	}
	return result
}

func workIntakeProposalOutcome(value v1.WorkIntakeProposalOutcome) error {
	switch value.Kind {
	case v1.WorkIntakeProposalSucceeded:
		if value.Proposal == nil || value.Code != "" {
			return fmt.Errorf("succeeded work intake proposal outcome is invalid")
		}
		return workIntakeProposal(*value.Proposal)
	case v1.WorkIntakeProposalRetryableFailure:
		if value.Proposal != nil ||
			(value.Code != v1.WorkIntakeProposalTemporarilyUnavailable && value.Code != v1.WorkIntakeProposalProviderUnavailable) {
			return fmt.Errorf("retryable work intake proposal outcome is invalid")
		}
	case v1.WorkIntakeProposalPermanentFailure:
		if value.Proposal != nil || value.Code != v1.WorkIntakeProposalInvalidOutput {
			return fmt.Errorf("permanent work intake proposal outcome is invalid")
		}
	case v1.WorkIntakeProposalUncertain:
		if value.Proposal != nil || value.Code != v1.WorkIntakeProposalGenerationUncertain {
			return fmt.Errorf("uncertain work intake proposal outcome is invalid")
		}
	default:
		return fmt.Errorf("work intake proposal outcome kind is unsupported")
	}
	return nil
}

func WorkIntakeProposalResultEvent(value v1.WorkIntakeProposalResultEvent) error {
	if value.Kind != v1.EventWorkIntakeProposalResult {
		return fmt.Errorf("work intake proposal result kind is invalid")
	}
	if err := workIntakeProposalAuthority(value.WorkIntakeProposalAuthority); err != nil {
		return err
	}
	if err := ID("command_message_id", value.CommandMessageID); err != nil {
		return err
	}
	if value.ObservedAt.IsZero() {
		return fmt.Errorf("work intake proposal observed_at is invalid")
	}
	return workIntakeProposalOutcome(value.Outcome)
}
