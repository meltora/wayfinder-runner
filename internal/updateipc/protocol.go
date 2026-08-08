package updateipc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/example/app-runner/internal/validation"
)

const (
	SchemaVersion   = 1
	MaxMessageBytes = 16 << 10
)

type Action string

const (
	ActionPrepare  Action = "prepare"
	ActionActivate Action = "activate"
	ActionHealth   Action = "health"
	ActionStatus   Action = "status"
)

// Request is intentionally capability-poor. In particular it has no URL,
// path, service, key, channel, target version, sequence, or force field.
// Privileged update policy belongs exclusively to the root updater.
type Request struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Action        Action `json:"action"`
	UpdateID      string `json:"update_id"`
	RunnerVersion string `json:"runner_version,omitempty"`
}

type Response struct {
	SchemaVersion int    `json:"schema_version"`
	OK            bool   `json:"ok"`
	Phase         string `json:"phase,omitempty"`
	UpdateID      string `json:"update_id,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
}

func (r Request) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported IPC schema version")
	}
	if err := validation.ID("request_id", r.RequestID); err != nil {
		return err
	}
	if err := validation.ID("update_id", r.UpdateID); err != nil {
		return err
	}
	switch r.Action {
	case ActionPrepare, ActionActivate, ActionStatus:
		if r.RunnerVersion != "" {
			return fmt.Errorf("runner_version is only valid for health requests")
		}
	case ActionHealth:
		if r.RunnerVersion == "" || len(r.RunnerVersion) > 128 {
			return fmt.Errorf("health request requires a bounded runner_version")
		}
	default:
		return fmt.Errorf("unsupported IPC action")
	}
	return nil
}

func (r Response) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported IPC schema version")
	}
	if r.UpdateID != "" {
		if err := validation.ID("update_id", r.UpdateID); err != nil {
			return err
		}
	}
	if r.Phase != "" {
		if err := validation.ID("phase", r.Phase); err != nil {
			return err
		}
	}
	if r.ErrorCode != "" {
		if err := validation.ID("error_code", r.ErrorCode); err != nil {
			return err
		}
	}
	if r.OK && r.ErrorCode != "" {
		return fmt.Errorf("successful response must not include error_code")
	}
	if !r.OK && r.ErrorCode == "" {
		return fmt.Errorf("failed response requires error_code")
	}
	return nil
}

func DecodeRequest(src io.Reader) (Request, error) {
	var request Request
	if err := decodeStrict(src, &request); err != nil {
		return Request{}, err
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func DecodeResponse(src io.Reader) (Response, error) {
	var response Response
	if err := decodeStrict(src, &response); err != nil {
		return Response{}, err
	}
	if err := response.Validate(); err != nil {
		return Response{}, err
	}
	return response, nil
}

func EncodeRequest(dst io.Writer, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	return encodeBounded(dst, request)
}

func EncodeResponse(dst io.Writer, response Response) error {
	if err := response.Validate(); err != nil {
		return err
	}
	return encodeBounded(dst, response)
}

func decodeStrict(src io.Reader, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(src, MaxMessageBytes+1))
	if err != nil {
		return fmt.Errorf("read IPC message: %w", err)
	}
	if len(raw) == 0 || len(raw) > MaxMessageBytes {
		return fmt.Errorf("IPC message size is invalid")
	}
	if err := rejectDuplicateFields(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode IPC message: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode IPC message: trailing data")
	}
	return nil
}

func rejectDuplicateFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode IPC message: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("IPC message must be a JSON object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode IPC field: %w", err)
		}
		name, ok := field.(string)
		if !ok || seen[name] {
			return fmt.Errorf("IPC message contains a duplicate or invalid field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode IPC field %s: %w", name, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode IPC message: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode IPC message: trailing data")
	}
	return nil
}

func encodeBounded(dst io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode IPC message: %w", err)
	}
	if len(raw)+1 > MaxMessageBytes {
		return fmt.Errorf("IPC message exceeds size limit")
	}
	raw = append(raw, '\n')
	if _, err := dst.Write(raw); err != nil {
		return fmt.Errorf("write IPC message: %w", err)
	}
	return nil
}
