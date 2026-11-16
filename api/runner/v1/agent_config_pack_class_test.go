package v1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAgentConfigScanMetadataRowsCarryPackClassesTogether(t *testing.T) {
	raw, err := os.ReadFile("testdata/agent_config_scan_v1_result.json")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	_ = json.Unmarshal(raw, &env)
	result := env["payload"].(map[string]any)
	scan := result["outcome"].(map[string]any)["scan"].(map[string]any)
	item := map[string]any{"path": "AGENTS.md", "source_type": "repository_instructions", "digest_sha256": strings.Repeat("b", 64), "size_bytes": 1, "classification": "ready", "risk": "low", "compatibility": "compatible", "decision": "not_required", "requested_permissions": []string{}, "conflicts": []string{}, "pack_class": "portable_guidance", "pack_reason_code": "instructions_portable"}
	scan["items"] = []any{item}
	payload, _ := json.Marshal(result)
	var decoded AgentConfigScanResultEvent
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Outcome.Scan.Items[0]
	if got.PackClass != AgentConfigPackPortableGuidance || got.PackReasonCode != AgentConfigPackReasonInstructionsPortable {
		t.Fatalf("pack fields = %+v", got)
	}
	delete(item, "pack_reason_code")
	payload, _ = json.Marshal(result)
	if json.Unmarshal(payload, &decoded) == nil {
		t.Fatal("pack class without reason code accepted")
	}
	item["pack_reason_code"] = ""
	payload, _ = json.Marshal(result)
	if json.Unmarshal(payload, &decoded) == nil {
		t.Fatal("empty pack reason code accepted")
	}
}

func TestAgentConfigPackReasonRulesCoverEveryClass(t *testing.T) {
	seen := map[AgentConfigPackClass]bool{}
	for code, rule := range AgentConfigPackReasonRules {
		if code == "" || len(rule.SourceTypes) == 0 {
			t.Fatalf("incomplete rule %q", code)
		}
		seen[rule.Class] = true
	}
	for _, class := range []AgentConfigPackClass{AgentConfigPackPortableGuidance, AgentConfigPackProviderOverlay, AgentConfigPackElevated, AgentConfigPackUnsupported, AgentConfigPackRejected, AgentConfigPackManualReview} {
		if !seen[class] {
			t.Fatalf("class %s has no reason code", class)
		}
	}
}
