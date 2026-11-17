package v1

import (
	"encoding/json"
	"fmt"
	"unicode/utf16"
)

const (
	ProtocolFeatureAgentConfigContentCapture = "agent_config_content_capture_v1"
	AgentConfigContentCaptureVersion         = "agent-config-content-v1"
	AgentConfigContentMaxItemBytes           = 64 << 10
	AgentConfigContentMaxTotalBytes          = 512 << 10
	AgentConfigContentMaxItems               = 512
)

type AgentConfigContentCapture struct {
	Version       string `json:"version"`
	MaxItemBytes  int    `json:"max_item_bytes"`
	MaxTotalBytes int    `json:"max_total_bytes"`
}

type AgentConfigItemContent struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

func (capture *AgentConfigContentCapture) UnmarshalJSON(raw []byte) error {
	if _, err := scanWireObject(raw, []string{"version", "max_item_bytes", "max_total_bytes"}, nil); err != nil {
		return err
	}
	type plainCapture AgentConfigContentCapture
	var decoded plainCapture
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*capture = AgentConfigContentCapture(decoded)
	return nil
}

func (content *AgentConfigItemContent) UnmarshalJSON(raw []byte) error {
	if _, err := scanWireObject(raw, []string{"path", "text"}, nil); err != nil {
		return err
	}
	type plainContent AgentConfigItemContent
	var decoded plainContent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*content = AgentConfigItemContent(decoded)
	return nil
}

func scanItemContentsWireField(raw json.RawMessage) error {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	if entries == nil {
		return fmt.Errorf("item_contents must be an array")
	}
	return nil
}

func AgentConfigContentPathLess(left, right string) bool {
	a, b := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
	for index := 0; index < len(a) && index < len(b); index++ {
		if a[index] != b[index] {
			return a[index] < b[index]
		}
	}
	return len(a) < len(b)
}
