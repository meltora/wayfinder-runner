package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
)

var agentConfigScanAuthorityKeys = []string{"authority_version", "request_id", "attempt_id", "project_id", "repository_id", "branch_ref", "lease_id", "fencing_token", "lease_expires_at", "command_digest"}

// encoding/json otherwise accepts case-insensitive aliases for struct fields.
// This transport uses the same exact canonical keys as the control plane.
func scanWireObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if fields[key] == nil {
			return nil, fmt.Errorf("missing scan field %s", key)
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key, value := range fields {
		if !allowed[key] || bytes.Equal(value, []byte("null")) {
			return nil, fmt.Errorf("invalid scan field %s", key)
		}
	}
	return fields, nil
}
func (authority *AgentConfigScanAuthority) UnmarshalJSON(raw []byte) error {
	if _, err := scanWireObject(raw, agentConfigScanAuthorityKeys, nil); err != nil {
		return err
	}
	type plainAuthority AgentConfigScanAuthority
	var decoded plainAuthority
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*authority = AgentConfigScanAuthority(decoded)
	return nil
}
func (event *AgentConfigScanResultEvent) UnmarshalJSON(raw []byte) error {
	keys := append(append([]string{}, agentConfigScanAuthorityKeys...), "kind", "command_message_id", "observed_at", "outcome")
	if _, err := scanWireObject(raw, keys, nil); err != nil {
		return err
	}
	// A method-free embedded authority prevents its UnmarshalJSON method from
	// consuming the outer result object through Go's method promotion.
	type plainAuthority AgentConfigScanAuthority
	var decoded struct {
		Kind string `json:"kind"`
		plainAuthority
		CommandMessageID string                 `json:"command_message_id"`
		ObservedAt       string                 `json:"observed_at"`
		Outcome          AgentConfigScanOutcome `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*event = AgentConfigScanResultEvent{Kind: decoded.Kind, AgentConfigScanAuthority: AgentConfigScanAuthority(decoded.plainAuthority), CommandMessageID: decoded.CommandMessageID, ObservedAt: decoded.ObservedAt, Outcome: decoded.Outcome}
	return nil
}
func scanDiscoveryWireFields(raw []byte) error {
	fields, err := scanWireObject(raw, []string{"kind", "scan_id", "project_id", "repository_id", "branch_ref", "commit_sha", "catalog_version", "parser_version", "observed_at", "items"}, nil)
	if err != nil {
		return err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(fields["items"], &items); err != nil {
		return err
	}
	for _, item := range items {
		fields, err := scanWireObject(item, []string{"path", "source_type", "size_bytes", "classification", "risk", "compatibility", "decision", "requested_permissions", "conflicts"}, []string{"digest_sha256", "provider", "reason", "pack_class", "pack_reason_code"})
		if err != nil {
			return err
		}
		if (fields["pack_class"] == nil) != (fields["pack_reason_code"] == nil) {
			return fmt.Errorf("pack_class and pack_reason_code must be sent together")
		}
		for _, key := range []string{"digest_sha256", "provider", "reason", "pack_class", "pack_reason_code"} {
			if value, present := fields[key]; present {
				var text string
				if json.Unmarshal(value, &text) != nil || text == "" {
					return fmt.Errorf("invalid optional scan field %s", key)
				}
			}
		}
	}
	return nil
}
