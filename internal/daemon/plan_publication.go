package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/validation"
)

const (
	publishPlanTool         = "platform_publish_plan"
	publishPlanRevisionTool = "platform_publish_plan_revision"
	maxPlanChangeSummary    = 16 << 10
)

type planPublicationArguments struct {
	Plan           v1.PlanContent `json:"plan"`
	ChangeSummary  string         `json:"change_summary"`
	IdempotencyKey string         `json:"idempotency_key"`
	BaseRevisionID string         `json:"base_revision_id,omitempty"`
}

type planPublicationIntent struct {
	Tool           string         `json:"tool"`
	BaseRevisionID string         `json:"base_revision_id,omitempty"`
	Plan           v1.PlanContent `json:"plan"`
	ChangeSummary  string         `json:"change_summary"`
}

type planPublishedEvent struct {
	Kind           string         `json:"kind"`
	ManifestKind   v1.JobKind     `json:"manifest_kind"`
	LeaseID        string         `json:"lease_id"`
	JobID          string         `json:"job_id"`
	AttemptID      string         `json:"attempt_id"`
	ManifestID     string         `json:"manifest_id"`
	ManifestDigest string         `json:"manifest_digest"`
	FencingToken   uint64         `json:"fencing_token"`
	ObservedAt     time.Time      `json:"observed_at"`
	BaseRevisionID string         `json:"base_revision_id,omitempty"`
	Plan           v1.PlanContent `json:"plan"`
	ChangeSummary  string         `json:"change_summary"`
}

type planPublicationRecoveryEnvelope struct {
	Version   int             `json:"version"`
	Type      v1.MessageType  `json:"type"`
	ID        string          `json:"id"`
	SentAt    time.Time       `json:"sent_at"`
	RunnerID  string          `json:"runner_id,omitempty"`
	InReplyTo string          `json:"in_reply_to,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

func isPlanPublicationTool(tool string) bool {
	return tool == publishPlanTool || tool == publishPlanRevisionTool
}

func planPublicationEventID(attemptID, manifestDigest, tool, key string) string {
	sum := sha256.Sum256([]byte("plan-publication-v1\x00" + attemptID + "\x00" + manifestDigest + "\x00" + tool + "\x00" + key))
	return "plan-" + hex.EncodeToString(sum[:16])
}

func (jr *jobRunner) handlePlanPublication(ctx context.Context, tool, key string, raw map[string]any) (any, []byte, error) {
	claim, err := jr.preparePlanPublicationClaim(tool, key, raw)
	if err != nil {
		return nil, nil, err
	}
	stored, _, err := jr.d.journal.ClaimPlanPublication(ctx, claim)
	if err != nil {
		return nil, nil, err
	}
	event, err := jr.validatePlanPublicationPayload(stored.Payload, tool)
	if err != nil {
		return nil, nil, fmt.Errorf("validate durable plan publication: %w", err)
	}
	state, completed, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, tool, key)
	if err != nil {
		return nil, nil, fmt.Errorf("record plan publication tool call: %w", err)
	}
	if state == journal.ToolCallCompleted {
		result, decodeErr := decodeToolResult(completed)
		return result, completed, decodeErr
	}
	if _, err := jr.d.sendEventWithStableID(ctx, stored.EventID, event.asMap()); err != nil {
		return nil, nil, err
	}
	if _, err := jr.recoverPlanPublication(ctx, stored.EventID, tool); err != nil {
		return nil, nil, err
	}
	result := map[string]any{"published": true, "event_id": stored.EventID}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, nil, fmt.Errorf("encode plan publication result: %w", err)
	}
	if err := jr.d.journal.CompleteToolCall(ctx, jr.job.AttemptID, tool, key, encoded); err != nil {
		state, existing, beginErr := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, tool, key)
		if beginErr == nil && state == journal.ToolCallCompleted {
			decoded, decodeErr := decodeToolResult(existing)
			return decoded, existing, decodeErr
		}
		return nil, nil, fmt.Errorf("commit plan publication result: %w", err)
	}
	return result, encoded, nil
}

func (jr *jobRunner) preparePlanPublicationClaim(tool, key string, raw map[string]any) (journal.PlanPublicationClaim, error) {
	if jr.job.Kind != v1.JobKindPlan {
		return journal.PlanPublicationClaim{}, fmt.Errorf("plan publication is available to plan jobs only")
	}
	var arguments planPublicationArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil {
		return journal.PlanPublicationClaim{}, err
	}
	if arguments.IdempotencyKey != key {
		return journal.PlanPublicationClaim{}, fmt.Errorf("plan publication idempotency key changed during validation")
	}
	if err := validatePlanChangeSummary(arguments.ChangeSummary); err != nil {
		return journal.PlanPublicationClaim{}, err
	}
	if err := validation.PlanContent(arguments.Plan); err != nil {
		return journal.PlanPublicationClaim{}, err
	}
	revisionContext := jr.job.PlanRevisionContext
	switch tool {
	case publishPlanTool:
		if revisionContext != nil {
			return journal.PlanPublicationClaim{}, fmt.Errorf("revision manifest must use %s", publishPlanRevisionTool)
		}
		if arguments.BaseRevisionID != "" {
			return journal.PlanPublicationClaim{}, fmt.Errorf("initial plan cannot declare base_revision_id")
		}
	case publishPlanRevisionTool:
		if revisionContext == nil {
			return journal.PlanPublicationClaim{}, fmt.Errorf("plan revision requires immutable plan_revision_context")
		}
		if err := validation.ID("base_revision_id", arguments.BaseRevisionID); err != nil {
			return journal.PlanPublicationClaim{}, err
		}
		if arguments.BaseRevisionID != revisionContext.BasePlan.RevisionID {
			return journal.PlanPublicationClaim{}, fmt.Errorf("base_revision_id does not match the reviewed base plan")
		}
	default:
		return journal.PlanPublicationClaim{}, fmt.Errorf("unsupported plan publication tool %q", tool)
	}

	intent := planPublicationIntent{
		Tool: tool, BaseRevisionID: arguments.BaseRevisionID,
		Plan: arguments.Plan, ChangeSummary: arguments.ChangeSummary,
	}
	intentRaw, err := json.Marshal(intent)
	if err != nil {
		return journal.PlanPublicationClaim{}, err
	}
	intentDigest := sha256.Sum256(intentRaw)
	event := planPublishedEvent{
		Kind: "plan_published", ManifestKind: v1.JobKindPlan,
		LeaseID: jr.job.LeaseID, JobID: jr.job.JobID, AttemptID: jr.job.AttemptID,
		ManifestID: jr.job.ManifestID, ManifestDigest: jr.job.ManifestDigest,
		FencingToken: jr.job.FencingToken, ObservedAt: time.Now().UTC(),
		BaseRevisionID: arguments.BaseRevisionID, Plan: arguments.Plan,
		ChangeSummary: arguments.ChangeSummary,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return journal.PlanPublicationClaim{}, err
	}
	return journal.PlanPublicationClaim{
		AttemptID: jr.job.AttemptID, Tool: tool, IdempotencyKey: key,
		EventID:     planPublicationEventID(jr.job.AttemptID, jr.job.ManifestDigest, tool, key),
		InputDigest: hex.EncodeToString(intentDigest[:]), Payload: payload,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (jr *jobRunner) recoverPlanPublication(ctx context.Context, eventID, tool string) (planPublishedEvent, error) {
	record, ok, err := jr.d.journal.LoadRecord(ctx, eventID)
	if err != nil {
		return planPublishedEvent{}, fmt.Errorf("load plan event: %w", err)
	}
	if !ok || record.Kind != journal.KindEvent {
		return planPublishedEvent{}, fmt.Errorf("plan event is not durably recorded")
	}
	var envelope planPublicationRecoveryEnvelope
	if err := decodeStrictJSON(record.Payload, &envelope); err != nil {
		return planPublishedEvent{}, fmt.Errorf("decode plan event envelope: %w", err)
	}
	if envelope.Version != v1.ProtocolVersion || envelope.Type != v1.TypeEvent || envelope.ID != eventID || envelope.SentAt.IsZero() {
		return planPublishedEvent{}, fmt.Errorf("plan event envelope identity is invalid")
	}
	return jr.validatePlanPublicationPayload(envelope.Payload, tool)
}

func (jr *jobRunner) validatePlanPublicationPayload(raw []byte, tool string) (planPublishedEvent, error) {
	if !isPlanPublicationTool(tool) || jr.job.Kind != v1.JobKindPlan {
		return planPublishedEvent{}, fmt.Errorf("invalid plan publication tool or job kind")
	}
	var event planPublishedEvent
	if err := decodeStrictJSON(raw, &event); err != nil {
		return planPublishedEvent{}, err
	}
	if event.Kind != "plan_published" || event.ManifestKind != v1.JobKindPlan ||
		event.LeaseID != jr.job.LeaseID || event.JobID != jr.job.JobID ||
		event.AttemptID != jr.job.AttemptID || event.ManifestID != jr.job.ManifestID ||
		event.ManifestDigest != jr.job.ManifestDigest || event.FencingToken != jr.job.FencingToken ||
		event.ObservedAt.IsZero() {
		return planPublishedEvent{}, fmt.Errorf("plan publication authority does not match this attempt")
	}
	if err := validatePlanChangeSummary(event.ChangeSummary); err != nil {
		return planPublishedEvent{}, err
	}
	if err := validation.PlanContent(event.Plan); err != nil {
		return planPublishedEvent{}, err
	}
	if tool == publishPlanTool && event.BaseRevisionID != "" {
		return planPublishedEvent{}, fmt.Errorf("initial plan event contains a base revision")
	}
	if tool == publishPlanRevisionTool {
		if jr.job.PlanRevisionContext == nil || event.BaseRevisionID != jr.job.PlanRevisionContext.BasePlan.RevisionID {
			return planPublishedEvent{}, fmt.Errorf("plan revision event is not bound to the reviewed base")
		}
	}
	return event, nil
}

func validatePlanChangeSummary(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("change_summary is required")
	}
	if len(value) > maxPlanChangeSummary || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("change_summary exceeds its size limit or contains NUL")
	}
	return nil
}

func decodeStrictToolArguments(raw map[string]any, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode plan publication arguments: %w", err)
	}
	if err := decodeStrictJSON(encoded, target); err != nil {
		return fmt.Errorf("invalid plan publication arguments: %w", err)
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("trailing JSON is not allowed")
	}
	return nil
}

func (event planPublishedEvent) asMap() map[string]any {
	payload := map[string]any{
		"kind": event.Kind, "manifest_kind": string(event.ManifestKind),
		"lease_id": event.LeaseID, "job_id": event.JobID, "attempt_id": event.AttemptID,
		"manifest_id": event.ManifestID, "manifest_digest": event.ManifestDigest,
		"fencing_token": event.FencingToken, "observed_at": event.ObservedAt.UTC().Format(time.RFC3339Nano),
		"plan": event.Plan, "change_summary": event.ChangeSummary,
	}
	if event.BaseRevisionID != "" {
		payload["base_revision_id"] = event.BaseRevisionID
	}
	return payload
}
