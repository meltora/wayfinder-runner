package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func commandContentDigest(payload any) (string, error) {
	return v1.CommandContentDigest(payload)
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
	if decoder.Decode(&struct{}{}) != io.EOF {
		return v1.CommandPayload{}, fmt.Errorf("decode command payload: trailing JSON")
	}
	return command, nil
}

func commandReceiptTerminal(receipt journal.CommandReceipt) bool {
	return receipt.State == journal.CommandAccepted || receipt.State == journal.CommandRejected
}

func (d *Daemon) sendDurableCommandAck(ctx context.Context, receipt journal.CommandReceipt) {
	var commandErr error
	if receipt.AckError != "" {
		commandErr = fmt.Errorf("%s", receipt.AckError)
	}
	if err := d.sendCommandAck(ctx, receipt.ID, receipt.AckStatus, commandErr); err != nil {
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
