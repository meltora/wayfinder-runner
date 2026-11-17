package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	AgentConfigScanTransportVersion = "agent-config-scan-v1"
	CmdAgentConfigScan              = "agent_config_scan"
	EventAgentConfigScanResult      = "agent_config_scan_result"
)

// AgentConfigScanAuthority carries only control-plane identifiers. Repository
// transport and filesystem access remain administrator-owned runner settings.
type AgentConfigScanAuthority struct {
	AuthorityVersion string `json:"authority_version"`
	RequestID        string `json:"request_id"`
	AttemptID        string `json:"attempt_id"`
	ProjectID        string `json:"project_id"`
	RepositoryID     string `json:"repository_id"`
	BranchRef        string `json:"branch_ref"`
	LeaseID          string `json:"lease_id"`
	FencingToken     uint64 `json:"fencing_token"`
	LeaseExpiresAt   string `json:"lease_expires_at"`
	CommandDigest    string `json:"command_digest"`
}
type AgentConfigScanOutcome struct {
	Kind         string                     `json:"kind"`
	Scan         *AgentConfigDiscoveryEvent `json:"scan,omitempty"`
	Code         string                     `json:"code,omitempty"`
	ItemContents []AgentConfigItemContent   `json:"item_contents,omitempty"`
}
type AgentConfigScanResultEvent struct {
	Kind string `json:"kind"`
	AgentConfigScanAuthority
	CommandMessageID string                 `json:"command_message_id"`
	ObservedAt       string                 `json:"observed_at"`
	Outcome          AgentConfigScanOutcome `json:"outcome"`
}

func AgentConfigScanCommandDigest(a AgentConfigScanAuthority) string {
	raw, _ := json.Marshal([]any{a.AuthorityVersion, a.RequestID, a.AttemptID, a.ProjectID, a.RepositoryID, a.BranchRef, a.LeaseID, a.FencingToken, a.LeaseExpiresAt})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func AgentConfigScanCommandMessageID(requestID string) string {
	return "agent-config-scan-command-" + requestID
}
func AgentConfigScanResultEventID(commandMessageID string) string {
	sum := sha256.Sum256([]byte(commandMessageID))
	return "agent-config-scan-result-" + hex.EncodeToString(sum[:])
}

// Distinguish absent fields from explicit null/empty foreign union fields.
// Semantic scope and bounded value validation remains at the protocol boundary.
func (outcome *AgentConfigScanOutcome) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	var kind string
	if err := json.Unmarshal(fields["kind"], &kind); err != nil {
		return err
	}
	expected := 2
	if contents, present := fields["item_contents"]; present {
		if kind != "discovered" {
			return fmt.Errorf("only a discovered outcome can carry item contents")
		}
		if err := scanItemContentsWireField(contents); err != nil {
			return err
		}
		expected = 3
	}
	if len(fields) != expected {
		return fmt.Errorf("scan outcome has unexpected fields")
	}
	switch kind {
	case "discovered":
		if fields["scan"] == nil || bytes.Equal(fields["scan"], []byte("null")) {
			return fmt.Errorf("discovered outcome requires scan")
		}
		if err := scanDiscoveryWireFields(fields["scan"]); err != nil {
			return err
		}
	case "failed":
		if fields["code"] == nil || bytes.Equal(fields["code"], []byte("null")) {
			return fmt.Errorf("failed outcome requires code")
		}
	default:
		return fmt.Errorf("unsupported scan outcome")
	}
	type plainOutcome AgentConfigScanOutcome
	var decoded plainOutcome
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*outcome = AgentConfigScanOutcome(decoded)
	return nil
}
