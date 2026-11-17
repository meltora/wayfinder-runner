package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/validation"
	"io"
	"time"
)

// A processing receipt is an uncertain boundary. Never repeat Git after a
// restart: reuse the exact stored result, or finish with an interrupted result.
func (d *Daemon) completeAgentConfigScanCommand(ctx context.Context, env v1.Envelope, command v1.CommandPayload, owner string, uncertain bool) {
	defer d.agentConfigScanCommandFinish(env.ID)
	err := validateAgentConfigScanCommandBoundary(command)
	if err == nil && env.ID != v1.AgentConfigScanCommandMessageID(command.AgentConfigScan.RequestID) {
		err = rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err != nil {
		d.finishAgentConfigScanReceipt(ctx, env.ID, owner, err)
		return
	}
	a := *command.AgentConfigScan
	found, err := d.hasAgentConfigScanResult(ctx, env.ID, a)
	if err != nil {
		d.logger.Error("read durable agent configuration scan result", "id", env.ID)
		return
	}
	if found {
		d.finishAgentConfigScanReceipt(ctx, env.ID, owner, nil)
		return
	}
	outcome := scanFailure("interrupted")
	if !uncertain {
		scanCtx, finish, startErr := d.beginAgentConfigScan(ctx, a)
		if startErr != nil {
			d.finishAgentConfigScanReceipt(ctx, env.ID, owner, startErr)
			return
		}
		outcome = d.scanAgentConfiguration(scanCtx, a, env.ProviderCredential, command.ContentCapture)
		outcome = d.finalizeAgentConfigScan(scanCtx, outcome)
		finish()
	}
	event := scanResultEvent(a, env.ID, outcome)
	if err := validation.AgentConfigScanResultEvent(event); err != nil {
		d.logger.Error("invalid agent configuration scan result", "id", env.ID)
		return
	}
	// Cancellation stops scanning, not the bounded durable recording of its outcome.
	durableCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := d.sendEventWithStableID(durableCtx, v1.AgentConfigScanResultEventID(env.ID), event); err != nil {
		d.logger.Error("persist agent configuration scan result", "id", env.ID)
		return
	}
	d.finishAgentConfigScanReceipt(durableCtx, env.ID, owner, nil)
}
func (d *Daemon) finishAgentConfigScanReceipt(ctx context.Context, id, owner string, executionErr error) {
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	status := v1.CommandAckAccepted
	if executionErr != nil {
		status = v1.CommandAckRejected
	}
	receipt, err := d.journal.CompleteCommand(bounded, id, owner, string(status), commandRejectionReason(executionErr))
	if err != nil {
		d.logger.Error("persist agent configuration scan receipt", "id", id)
		return
	}
	d.sendDurableCommandAck(bounded, receipt)
}
func (d *Daemon) hasAgentConfigScanResult(ctx context.Context, id string, a v1.AgentConfigScanAuthority) (bool, error) {
	record, found, err := d.journal.LoadRecord(ctx, v1.AgentConfigScanResultEventID(id))
	if err != nil || !found {
		return false, err
	}
	if record.Kind != journal.KindEvent {
		return false, fmt.Errorf("unexpected scan result record kind")
	}
	var wire struct {
		Version  int                           `json:"version"`
		Type     v1.MessageType                `json:"type"`
		ID       string                        `json:"id"`
		SentAt   time.Time                     `json:"sent_at"`
		RunnerID string                        `json:"runner_id,omitempty"`
		Payload  v1.AgentConfigScanResultEvent `json:"payload"`
	}
	decoder := json.NewDecoder(bytes.NewReader(record.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(&struct{}{}) != io.EOF || wire.Version != v1.ProtocolVersion || wire.Type != v1.TypeEvent || wire.ID != record.ID || wire.RunnerID != d.runnerID || wire.SentAt.IsZero() {
		return false, fmt.Errorf("invalid stored scan event")
	}
	if err := validation.AgentConfigScanResultEvent(wire.Payload); err != nil {
		return false, err
	}
	if wire.Payload.AgentConfigScanAuthority != a || wire.Payload.CommandMessageID != id {
		return false, fmt.Errorf("stored scan result authority mismatch")
	}
	return true, nil
}
