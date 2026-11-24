package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func commandContentDigest(payload any) (string, error) {
	return v1.CommandContentDigest(payload)
}

// durableCommandRecordPayload keeps work-intake content memory-only. The
// receipt table's independently computed content digest remains authoritative
// for exact-redelivery conflict detection; this record retains only the
// routing/fence fields needed to audit the paid-call fence and stable result.
func durableCommandRecordPayload(envelope v1.Envelope, contentDigest string) ([]byte, error) {
	envelope.ProviderCredential = nil
	envelope.PreviewSecrets = nil
	rawPayload, err := json.Marshal(envelope.Payload)
	if err != nil {
		return nil, err
	}
	var discriminator struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(rawPayload, &discriminator) != nil || discriminator.Command != v1.CmdWorkIntakeProposal {
		return json.Marshal(envelope)
	}
	command, _ := decodeCommandPayload(envelope.Payload)
	redacted := struct {
		Version  int            `json:"version"`
		Type     v1.MessageType `json:"type"`
		ID       string         `json:"id"`
		SentAt   time.Time      `json:"sent_at"`
		RunnerID string         `json:"runner_id,omitempty"`
		Receipt  struct {
			Command             string                          `json:"command"`
			Authority           *v1.WorkIntakeProposalAuthority `json:"authority,omitempty"`
			ContentDigest       string                          `json:"content_digest"`
			StableResultEventID string                          `json:"stable_result_event_id"`
		} `json:"work_intake_proposal_receipt"`
	}{
		Version: envelope.Version, Type: envelope.Type, ID: envelope.ID,
		SentAt: envelope.SentAt, RunnerID: envelope.RunnerID,
	}
	redacted.Receipt.Command = v1.CmdWorkIntakeProposal
	redacted.Receipt.ContentDigest = contentDigest
	redacted.Receipt.StableResultEventID = workIntakeProposalResultEventID(envelope.ID)
	if command.WorkIntakeProposal != nil {
		authority := command.WorkIntakeProposal.WorkIntakeProposalAuthority
		redacted.Receipt.Authority = &authority
	}
	return json.Marshal(redacted)
}

func decodeCommandPayload(payload any) (v1.CommandPayload, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return v1.CommandPayload{}, fmt.Errorf("decode command payload: %w", err)
	}
	var command v1.CommandPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return v1.CommandPayload{}, fmt.Errorf("decode command payload: %w", err)
	}
	if command.Command == v1.CmdAgentConfigScan {
		if err := validateAgentConfigScanWireFields(raw); err != nil {
			return v1.CommandPayload{}, err
		}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return v1.CommandPayload{}, fmt.Errorf("decode command payload: trailing JSON")
	}
	return command, nil
}

func commandReceiptTerminal(receipt journal.CommandReceipt) bool {
	return receipt.State == journal.CommandAccepted || receipt.State == journal.CommandRejected
}

func (d *Daemon) sendDurableCommandAck(ctx context.Context, receipt journal.CommandReceipt) {
	if err := d.sendCommandAck(
		ctx,
		receipt.ID,
		v1.CommandAckStatus(receipt.AckStatus),
		receipt.AckReasonCode,
	); err != nil {
		d.logger.Debug("send command ack", "id", receipt.ID, "err", err)
	}
}

func (d *Daemon) recordCommandAckAttempt(payload any, receipt journal.CommandReceipt) {
	if receipt.AckStatus != "accepted" {
		return
	}
	command, err := decodeCommandPayload(payload)
	if err != nil || command.Command != v1.CmdUpdate {
		return
	}
	if err := d.markUpdateAckAttempted(command.UpdateID); err != nil {
		d.logger.Error("persist update command acknowledgement attempt", "id", receipt.ID, "err", err)
	}
}
