package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/redact"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/validation"
)

func contentCapture() *v1.AgentConfigContentCapture {
	return &v1.AgentConfigContentCapture{Version: v1.AgentConfigContentCaptureVersion, MaxItemBytes: v1.AgentConfigContentMaxItemBytes, MaxTotalBytes: v1.AgentConfigContentMaxTotalBytes}
}

func contentInventory() scanner.Inventory {
	item := func(path, text string) v1.AgentConfigDiscoveryItem {
		sum := sha256.Sum256([]byte(text))
		return v1.AgentConfigDiscoveryItem{Path: path, SourceType: v1.AgentConfigRepositoryInstructions, SizeBytes: int64(len(text)), DigestSHA256: hex.EncodeToString(sum[:]), Classification: v1.AgentConfigClassReady, Risk: v1.AgentConfigRiskLow, Compatibility: v1.AgentConfigCompatible, Decision: v1.AgentConfigDecisionNotRequired, RequestedPermissions: []string{}, Conflicts: []string{}}
	}
	big := strings.Repeat("b", v1.AgentConfigContentMaxItemBytes+1)
	return scanner.Inventory{
		Items:    []v1.AgentConfigDiscoveryItem{item("AGENTS.md", "# Guide\n"), item("CLAUDE.md", big), item("GEMINI.md", "uses remembered-secret-value-123\n")},
		Contents: map[string]string{"AGENTS.md": "# Guide\n", "CLAUDE.md": big, "GEMINI.md": "uses remembered-secret-value-123\n"},
	}
}

func runContentScan(t *testing.T, capture *v1.AgentConfigContentCapture) (v1.AgentConfigScanResultEvent, []byte) {
	t.Helper()
	redact.ForgetSecretsForTest()
	t.Cleanup(redact.ForgetSecretsForTest)
	redact.RememberSecret("remembered-secret-value-123")
	d := scanDaemon(t)
	d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		return strings.Repeat("a", 40), contentInventory(), nil
	}
	c := validScanCommand()
	c.ContentCapture = capture
	env := scanEnvelope(c)
	if err := d.OnCommand(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	record, ok, err := d.journal.LoadRecord(context.Background(), v1.AgentConfigScanResultEventID(env.ID))
	if err != nil || !ok {
		t.Fatalf("missing durable result: %v", err)
	}
	return scanResult(t, d, env.ID), record.Payload
}

func TestAgentConfigScanSendsRedactedDigestBoundContentWhenRequested(t *testing.T) {
	result, _ := runContentScan(t, contentCapture())
	if result.Outcome.Kind != "discovered" {
		t.Fatalf("outcome=%+v", result.Outcome)
	}
	want := []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: "# Guide\n"}}
	if !reflect.DeepEqual(result.Outcome.ItemContents, want) {
		t.Fatalf("item contents=%q", result.Outcome.ItemContents)
	}
	if err := validation.AgentConfigScanResultEvent(result); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfigScanWithoutContentCaptureOmitsItemContents(t *testing.T) {
	result, raw := runContentScan(t, nil)
	if result.Outcome.Kind != "discovered" || result.Outcome.ItemContents != nil {
		t.Fatalf("outcome=%+v", result.Outcome)
	}
	if strings.Contains(string(raw), "item_contents") || strings.Contains(string(raw), "# Guide") {
		t.Fatalf("content leaked without capture: %s", raw)
	}
}

func TestAgentConfigScanContentCaptureWireBoundary(t *testing.T) {
	c := validScanCommand()
	c.ContentCapture = contentCapture()
	decoded, err := decodeCommandPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.ContentCapture, c.ContentCapture) {
		t.Fatalf("capture=%+v", decoded.ContentCapture)
	}
	if err := validateAgentConfigScanCommandBoundary(decoded); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	payload["content_capture"] = nil
	if _, err := decodeCommandPayload(payload); err == nil {
		t.Fatal("null capture accepted")
	}
	future := validScanCommand()
	future.ContentCapture = contentCapture()
	future.ContentCapture.Version = "agent-config-content-v2"
	if validateAgentConfigScanCommandBoundary(future) == nil {
		t.Fatal("unsupported capture version accepted")
	}
	foreign := v1.CommandPayload{Command: v1.CmdResumeRunner, ContentCapture: contentCapture()}
	if validateAgentConfigScanCommandBoundary(foreign) == nil {
		t.Fatal("capture accepted on a foreign command")
	}
}

func TestAgentConfigScanContentStaysWithinTheEnvelopeBudget(t *testing.T) {
	items := make([]v1.AgentConfigDiscoveryItem, 0, 9)
	contents := map[string]string{}
	text := strings.Repeat("<", v1.AgentConfigContentMaxItemBytes)
	sum := sha256.Sum256([]byte(text))
	for index := 0; index < 9; index++ {
		path := "skills/s" + string(rune('a'+index)) + "/SKILL.md"
		items = append(items, v1.AgentConfigDiscoveryItem{Path: path, Classification: v1.AgentConfigClassReady, DigestSHA256: hex.EncodeToString(sum[:])})
		contents[path] = text
	}
	outcome := withItemContents(v1.AgentConfigScanOutcome{Kind: "discovered", Scan: &v1.AgentConfigDiscoveryEvent{Items: items}}, scanner.Inventory{Items: items, Contents: contents}, contentCapture())
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemContents) == 0 || len(raw) > maxScanResultEnvelopeBytes-scanResultEnvelopeReserve {
		t.Fatalf("encoded outcome %d bytes with %d entries", len(raw), len(outcome.ItemContents))
	}
}
