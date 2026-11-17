package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentConfigContentCaptureConstantsMatchTheCloudContract(t *testing.T) {
	if ProtocolFeatureAgentConfigContentCapture != "agent_config_content_capture_v1" || AgentConfigContentCaptureVersion != "agent-config-content-v1" {
		t.Fatal("content capture identifiers drifted")
	}
	if AgentConfigContentMaxItemBytes != 64<<10 || AgentConfigContentMaxTotalBytes != 512<<10 || AgentConfigContentMaxItems != 512 {
		t.Fatal("content capture limits drifted")
	}
}

func TestCommandPayloadDecodesContentCaptureStrictly(t *testing.T) {
	var capture AgentConfigContentCapture
	if err := json.Unmarshal([]byte(`{"version":"agent-config-content-v1","max_item_bytes":65536,"max_total_bytes":524288}`), &capture); err != nil {
		t.Fatal(err)
	}
	if capture != (AgentConfigContentCapture{Version: AgentConfigContentCaptureVersion, MaxItemBytes: 65536, MaxTotalBytes: 524288}) {
		t.Fatalf("capture=%+v", capture)
	}
	for _, raw := range []string{
		`{"version":"agent-config-content-v1","max_item_bytes":65536}`,
		`{"version":"agent-config-content-v1","max_item_bytes":65536,"max_total_bytes":524288,"extra":1}`,
		`{"Version":"agent-config-content-v1","max_item_bytes":65536,"max_total_bytes":524288}`,
		`{"version":null,"max_item_bytes":65536,"max_total_bytes":524288}`,
	} {
		if json.Unmarshal([]byte(raw), &capture) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	encoded, err := json.Marshal(CommandPayload{Command: CmdAgentConfigScan})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "content_capture") {
		t.Fatalf("absent capture must be omitted: %s", encoded)
	}
}

func TestAgentConfigScanOutcomeCarriesItemContentsOnlyWhenDiscovered(t *testing.T) {
	scan := `{"kind":"agent_config_discovery","scan_id":"scan-1","project_id":"p","repository_id":"r","branch_ref":"refs/heads/main","commit_sha":"` + strings.Repeat("a", 40) + `","catalog_version":"c","parser_version":"p","observed_at":"2026-09-12T12:00:00Z","items":[]}`
	var outcome AgentConfigScanOutcome
	if err := json.Unmarshal([]byte(`{"kind":"discovered","scan":`+scan+`,"item_contents":[{"path":"AGENTS.md","text":"hi"}]}`), &outcome); err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemContents) != 1 || outcome.ItemContents[0] != (AgentConfigItemContent{Path: "AGENTS.md", Text: "hi"}) {
		t.Fatalf("outcome=%+v", outcome)
	}
	for name, raw := range map[string]string{
		"failed with contents": `{"kind":"failed","code":"scan_failed","item_contents":[]}`,
		"null contents":        `{"kind":"discovered","scan":` + scan + `,"item_contents":null}`,
		"foreign entry key":    `{"kind":"discovered","scan":` + scan + `,"item_contents":[{"path":"AGENTS.md","text":"hi","digest":"x"}]}`,
		"missing entry text":   `{"kind":"discovered","scan":` + scan + `,"item_contents":[{"path":"AGENTS.md"}]}`,
		"aliased entry key":    `{"kind":"discovered","scan":` + scan + `,"item_contents":[{"Path":"AGENTS.md","text":"hi"}]}`,
		"foreign outcome key":  `{"kind":"discovered","scan":` + scan + `,"other":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var decoded AgentConfigScanOutcome
			if json.Unmarshal([]byte(raw), &decoded) == nil {
				t.Fatal("accepted")
			}
		})
	}
	encoded, err := json.Marshal(AgentConfigScanOutcome{Kind: "failed", Code: "scan_failed"})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"kind":"failed","code":"scan_failed"}` {
		t.Fatalf("encoded=%s", encoded)
	}
}
