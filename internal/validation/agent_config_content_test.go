package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func contentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func contentResultEvent(items []v1.AgentConfigDiscoveryItem, contents []v1.AgentConfigItemContent) v1.AgentConfigScanResultEvent {
	a := scanAuthority()
	scan := v1.AgentConfigDiscoveryEvent{Kind: v1.EventKindAgentConfigDiscovery, ScanID: "scan-1", ProjectID: a.ProjectID, RepositoryID: a.RepositoryID, BranchRef: a.BranchRef, CommitSHA: strings.Repeat("a", 40), CatalogVersion: v1.AgentConfigCatalogVersion, ParserVersion: "runner-scanner-v1", ObservedAt: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC), Items: items}
	return v1.AgentConfigScanResultEvent{Kind: v1.EventAgentConfigScanResult, AgentConfigScanAuthority: a, CommandMessageID: v1.AgentConfigScanCommandMessageID(a.RequestID), ObservedAt: "2026-09-12T12:00:00Z", Outcome: v1.AgentConfigScanOutcome{Kind: "discovered", Scan: &scan, ItemContents: contents}}
}

func contentItem(path, text string) v1.AgentConfigDiscoveryItem {
	return v1.AgentConfigDiscoveryItem{Path: path, SourceType: v1.AgentConfigRepositoryInstructions, SizeBytes: int64(len(text)), DigestSHA256: contentDigest(text), Classification: v1.AgentConfigClassReady, Risk: v1.AgentConfigRiskLow, Compatibility: v1.AgentConfigCompatible, Decision: v1.AgentConfigDecisionNotRequired, RequestedPermissions: []string{}, Conflicts: []string{}}
}

func TestAgentConfigContentCaptureRequiresTheExactContract(t *testing.T) {
	valid := v1.AgentConfigContentCapture{Version: v1.AgentConfigContentCaptureVersion, MaxItemBytes: v1.AgentConfigContentMaxItemBytes, MaxTotalBytes: v1.AgentConfigContentMaxTotalBytes}
	if err := AgentConfigContentCapture(valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*v1.AgentConfigContentCapture){
		"version": func(c *v1.AgentConfigContentCapture) { c.Version = "agent-config-content-v2" },
		"item":    func(c *v1.AgentConfigContentCapture) { c.MaxItemBytes = 1 << 20 },
		"total":   func(c *v1.AgentConfigContentCapture) { c.MaxTotalBytes = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			change(&c)
			if AgentConfigContentCapture(c) == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestAgentConfigScanResultValidatesItemContents(t *testing.T) {
	items := []v1.AgentConfigDiscoveryItem{contentItem("AGENTS.md", "one"), contentItem("CLAUDE.md", "two")}
	rejected := v1.AgentConfigDiscoveryItem{Path: "z/.env", SourceType: v1.AgentConfigCredential, Classification: v1.AgentConfigClassRejected, Risk: v1.AgentConfigRiskCritical, Compatibility: v1.AgentConfigIncompatible, Decision: v1.AgentConfigDecisionRejected, RequestedPermissions: []string{}, Conflicts: []string{}}
	if err := AgentConfigScanResultEvent(contentResultEvent(append(items, rejected), []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: "one"}, {Path: "CLAUDE.md", Text: "two"}})); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", v1.AgentConfigContentMaxItemBytes+1)
	for name, tc := range map[string]struct {
		items    []v1.AgentConfigDiscoveryItem
		contents []v1.AgentConfigItemContent
	}{
		"unknown path":    {items, []v1.AgentConfigItemContent{{Path: "OTHER.md", Text: "one"}}},
		"digest mismatch": {items, []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: "changed"}}},
		"unsorted":        {items, []v1.AgentConfigItemContent{{Path: "CLAUDE.md", Text: "two"}, {Path: "AGENTS.md", Text: "one"}}},
		"duplicate":       {items, []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: "one"}, {Path: "AGENTS.md", Text: "one"}}},
		"rejected item":   {append(items, rejected), []v1.AgentConfigItemContent{{Path: "z/.env", Text: ""}}},
		"over item limit": {[]v1.AgentConfigDiscoveryItem{contentItem("AGENTS.md", big)}, []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: big}}},
		"invalid utf8":    {[]v1.AgentConfigDiscoveryItem{contentItem("AGENTS.md", "\xff")}, []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: "\xff"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if AgentConfigScanResultEvent(contentResultEvent(tc.items, tc.contents)) == nil {
				t.Fatal("accepted")
			}
		})
	}
	failed := contentResultEvent(items, nil)
	failed.Outcome = v1.AgentConfigScanOutcome{Kind: "failed", Code: "scan_failed", ItemContents: []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: "one"}}}
	if AgentConfigScanResultEvent(failed) == nil {
		t.Fatal("failed outcome carried contents")
	}
}

func TestAgentConfigScanResultBoundsTotalContent(t *testing.T) {
	var items []v1.AgentConfigDiscoveryItem
	var contents []v1.AgentConfigItemContent
	text := strings.Repeat("a", v1.AgentConfigContentMaxItemBytes)
	for index := 0; index < 9; index++ {
		path := "skills/s" + string(rune('a'+index)) + "/SKILL.md"
		items = append(items, contentItem(path, text))
		contents = append(contents, v1.AgentConfigItemContent{Path: path, Text: text})
	}
	if AgentConfigScanResultEvent(contentResultEvent(items, contents[:8])) != nil {
		t.Fatal("exact total rejected")
	}
	if AgentConfigScanResultEvent(contentResultEvent(items, contents)) == nil {
		t.Fatal("over total accepted")
	}
}

func TestAgentConfigContentPathOrderUsesUTF16CodeUnits(t *testing.T) {
	if v1.AgentConfigContentPathLess("a/Ａ", "a/\U0001F600") || !v1.AgentConfigContentPathLess("a/\U0001F600", "a/Ａ") || !v1.AgentConfigContentPathLess("AGENTS.md", "CLAUDE.md") || v1.AgentConfigContentPathLess("x", "x") {
		t.Fatal("UTF-16 ordering differs from the control plane")
	}
}
