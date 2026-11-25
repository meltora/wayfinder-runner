package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

const replayPageSize = 100

func (d *Daemon) replayUnacked(ctx context.Context, send func(v1.Envelope) error) error {
	total := 0
	through, err := d.journal.LatestSequence(ctx)
	if err != nil {
		return err
	}
replay:
	for cursor := journal.Sequence(0); cursor < through; {
		records, err := d.journal.UnackedAfter(ctx, journal.KindEvent, cursor, replayPageSize)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			if record.Sequence > through {
				break replay
			}
			cursor = record.Sequence
			envelope, valid := decodeReplayEnvelope(record)
			if !valid {
				d.logger.Error("discarding corrupt durable event", "record_id", record.ID)
				if err := d.journal.AckKind(ctx, record.ID, journal.KindEvent); err != nil {
					return err
				}
				continue
			}
			if err := send(envelope); err != nil {
				return err
			}
			total++
		}
	}
	if total > 0 {
		d.logger.Info("replayed unacked events", "count", total)
	}
	return nil
}

func decodeReplayEnvelope(record journal.Record) (v1.Envelope, bool) {
	var payload json.RawMessage
	envelope := v1.Envelope{Payload: &payload}
	decoder := json.NewDecoder(bytes.NewReader(record.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		envelope.ID != record.ID || envelope.Type != v1.TypeEvent || envelope.Version != v1.ProtocolVersion {
		return v1.Envelope{}, false
	}
	var raw struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(record.Payload, &raw); err != nil {
		return v1.Envelope{}, false
	}
	envelope.Payload = nil
	if len(raw.Payload) > 0 {
		envelope.Payload = raw.Payload
	}
	return envelope, true
}
