package v1

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAgentConfigScanCapabilityWireOptional(t *testing.T) {
	m := CapabilityManifest{AgentConfigScanTransportVersion: AgentConfigScanTransportVersion, AgentConfigScanRepositoryIDs: []string{"repository-1"}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["agent_config_scan_transport_version"] != AgentConfigScanTransportVersion {
		t.Fatal("missing scan capability")
	}
	raw, _ = json.Marshal(CapabilityManifest{})
	fields = map[string]any{}
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["agent_config_scan_transport_version"]; ok {
		t.Fatal("legacy runner advertised scan")
	}
}
func TestAgentConfigScanCommandDigestGolden(t *testing.T) {
	a := AgentConfigScanAuthority{AuthorityVersion: AgentConfigScanTransportVersion, RequestID: "12345678-1234-4234-8234-123456789abc", AttemptID: "attempt-1", ProjectID: "project-1", RepositoryID: "repository-1", BranchRef: "refs/heads/main", LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: "2026-09-12T12:10:00Z"}
	if got := AgentConfigScanCommandDigest(a); got != "8970f461b89ee739f0ddbfa604153d37ad2373cd90423af04618c707424a142f" {
		t.Fatalf("digest=%s", got)
	}
}

func TestAgentConfigScanEventAndCommandIDsAreStable(t *testing.T) {
	request := "12345678-1234-4234-8234-123456789abc"
	command := AgentConfigScanCommandMessageID(request)
	if command != "agent-config-scan-command-"+request {
		t.Fatal("command identity drift")
	}
	if got := AgentConfigScanResultEventID(command); got != AgentConfigScanResultEventID(command) || got == AgentConfigScanResultEventID("other") {
		t.Fatal("unstable result identity")
	}
}

func TestAgentConfigScanOutcomeStrictWire(t *testing.T) {
	for _, raw := range []string{`{"kind":"failed","code":"interrupted","scan":null}`, `{"kind":"discovered","scan":{},"code":""}`, `{"kind":"failed","code":"interrupted","extra":null}`, `{"kind":"failed"}`, `null`, `[]`, `{"kind":3,"code":"x"}`, `{"kind":"future","code":"x"}`, `{"kind":"discovered","extra":1}`, `{"kind":"failed","extra":1}`, `{"kind":"failed","code":3}`, `{"kind":"failed","code":null}`, `{"kind":"discovered","scan":null}`, `{"kind":"discovered","scan":{"unknown":true}}`} {
		var outcome AgentConfigScanOutcome
		if json.Unmarshal([]byte(raw), &outcome) == nil {
			t.Fatalf("invalid outcome accepted: %s", raw)
		}
	}
	for _, raw := range []string{`{"kind":"failed","code":"interrupted"}`} {
		var outcome AgentConfigScanOutcome
		if err := json.Unmarshal([]byte(raw), &outcome); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentConfigScanOutcomeRejectsInvalidDirectDecode(t *testing.T) {
	var outcome AgentConfigScanOutcome
	if outcome.UnmarshalJSON([]byte("{")) == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestAgentConfigScanWireRejectsCaseAliasesAndMissingFields(t *testing.T) {
	raw, err := os.ReadFile("testdata/agent_config_scan_v1_command.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	_ = json.Unmarshal(raw, &envelope)
	authority := envelope["payload"].(map[string]any)["agent_config_scan"].(map[string]any)
	for key, value := range authority {
		t.Run(key, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range authority {
				copy[k] = v
			}
			delete(copy, key)
			copy[strings.ToUpper(key)] = value
			bad, _ := json.Marshal(copy)
			var decoded AgentConfigScanAuthority
			if json.Unmarshal(bad, &decoded) == nil {
				t.Fatal("case-aliased authority accepted")
			}
			delete(copy, strings.ToUpper(key))
			bad, _ = json.Marshal(copy)
			if json.Unmarshal(bad, &decoded) == nil {
				t.Fatal("missing authority field accepted")
			}
		})
	}
	raw, err = os.ReadFile("testdata/agent_config_scan_v1_result.json")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &envelope)
	result := envelope["payload"].(map[string]any)
	for key, value := range result {
		t.Run("result/"+key, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range result {
				copy[k] = v
			}
			delete(copy, key)
			copy[strings.ToUpper(key)] = value
			bad, _ := json.Marshal(copy)
			var decoded AgentConfigScanResultEvent
			if json.Unmarshal(bad, &decoded) == nil {
				t.Fatal("case-aliased result accepted")
			}
		})
	}
	scan := result["outcome"].(map[string]any)["scan"].(map[string]any)
	scan["Items"] = scan["items"]
	delete(scan, "items")
	bad, _ := json.Marshal(result)
	var decoded AgentConfigScanResultEvent
	if json.Unmarshal(bad, &decoded) == nil {
		t.Fatal("case-aliased nested scan accepted")
	}
}

func TestAgentConfigScanCanonicalTypedWireRoundtrip(t *testing.T) {
	for _, file := range []string{"agent_config_scan_v1_command.json", "agent_config_scan_v1_result.json"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + file)
			if err != nil {
				t.Fatal(err)
			}
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(env.Payload)
			if file == "agent_config_scan_v1_command.json" {
				var c CommandPayload
				if err := json.Unmarshal(payload, &c); err != nil {
					t.Fatal(err)
				}
				if c.AgentConfigScan == nil || c.AgentConfigScan.ProjectID != "project-1" {
					t.Fatal("authority lost")
				}
				env.Payload = c
			} else {
				var result AgentConfigScanResultEvent
				if err := json.Unmarshal(payload, &result); err != nil {
					t.Fatal(err)
				}
				if result.Kind != EventAgentConfigScanResult || result.ProjectID != "project-1" || result.Outcome.Scan == nil {
					t.Fatal("embedded authority consumed outer result")
				}
				env.Payload = result
			}
			output, _ := json.Marshal(env)
			var expected, actual any
			_ = json.Unmarshal(raw, &expected)
			_ = json.Unmarshal(output, &actual)
			if !reflect.DeepEqual(expected, actual) {
				t.Fatalf("roundtrip changed fields: %s", output)
			}
		})
	}
}
func TestAgentConfigScanCanonicalMetadataRowsRemainStrict(t *testing.T) {
	raw, err := os.ReadFile("testdata/agent_config_scan_v1_result.json")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	_ = json.Unmarshal(raw, &env)
	result := env["payload"].(map[string]any)
	scan := result["outcome"].(map[string]any)["scan"].(map[string]any)
	item := map[string]any{"path": "AGENTS.md", "source_type": "repository_instructions", "digest_sha256": strings.Repeat("b", 64), "size_bytes": 1, "classification": "ready", "risk": "low", "compatibility": "compatible", "decision": "not_required", "provider": "codex", "requested_permissions": []string{}, "conflicts": []string{}}
	scan["items"] = []any{item}
	payload, _ := json.Marshal(result)
	var decoded AgentConfigScanResultEvent
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Outcome.Scan.Items) != 1 || decoded.Outcome.Scan.Items[0].DigestSHA256 != strings.Repeat("b", 64) {
		t.Fatal("canonical row changed")
	}
	for _, key := range []string{"provider", "reason", "digest_sha256"} {
		previous, present := item[key]
		item[key] = ""
		payload, _ = json.Marshal(result)
		if json.Unmarshal(payload, &decoded) == nil {
			t.Fatalf("empty optional %s accepted", key)
		}
		if present {
			item[key] = previous
		} else {
			delete(item, key)
		}
	}
	item["Path"] = item["path"]
	delete(item, "path")
	payload, _ = json.Marshal(result)
	if json.Unmarshal(payload, &decoded) == nil {
		t.Fatal("row alias accepted")
	}
	scan["items"] = "not-an-array"
	payload, _ = json.Marshal(result)
	if json.Unmarshal(payload, &decoded) == nil {
		t.Fatal("non-array items accepted")
	}
}
