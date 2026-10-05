package validation

import (
	"bytes"
	"encoding/json"
	v1 "github.com/example/app-runner/api/runner/v1"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func scanAuthority() v1.AgentConfigScanAuthority {
	a := v1.AgentConfigScanAuthority{AuthorityVersion: v1.AgentConfigScanTransportVersion, RequestID: "12345678-1234-4234-8234-123456789abc", AttemptID: "attempt-1", ProjectID: "project-1", RepositoryID: "repository-1", BranchRef: "refs/heads/main", LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: "2026-09-12T12:10:00Z"}
	a.CommandDigest = v1.AgentConfigScanCommandDigest(a)
	return a
}
func TestAgentConfigScanAuthority(t *testing.T) {
	if err := AgentConfigScanAuthority(scanAuthority()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*v1.AgentConfigScanAuthority){
		"version":          func(a *v1.AgentConfigScanAuthority) { a.AuthorityVersion = "future" },
		"uuid":             func(a *v1.AgentConfigScanAuthority) { a.RequestID = "request-1" },
		"attempt":          func(a *v1.AgentConfigScanAuthority) { a.AttemptID = "../escape" },
		"project":          func(a *v1.AgentConfigScanAuthority) { a.ProjectID = "" },
		"repository":       func(a *v1.AgentConfigScanAuthority) { a.RepositoryID = "" },
		"lease":            func(a *v1.AgentConfigScanAuthority) { a.LeaseID = "bad id" },
		"zero fence":       func(a *v1.AgentConfigScanAuthority) { a.FencingToken = 0 },
		"large fence":      func(a *v1.AgentConfigScanAuthority) { a.FencingToken = 1 << 53 },
		"fraction":         func(a *v1.AgentConfigScanAuthority) { a.LeaseExpiresAt = "2026-09-12T12:10:00.000Z" },
		"offset":           func(a *v1.AgentConfigScanAuthority) { a.LeaseExpiresAt = "2026-09-12T12:10:00+00:00" },
		"branch":           func(a *v1.AgentConfigScanAuthority) { a.BranchRef = "main" },
		"revision":         func(a *v1.AgentConfigScanAuthority) { a.BranchRef = "refs/heads/main~1" },
		"branch traversal": func(a *v1.AgentConfigScanAuthority) { a.BranchRef = "refs/heads/a/../b" },
		"unicode":          func(a *v1.AgentConfigScanAuthority) { a.BranchRef = "refs/heads/ž" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			a := scanAuthority()
			change(&a)
			a.CommandDigest = v1.AgentConfigScanCommandDigest(a)
			if AgentConfigScanAuthority(a) == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
	a := scanAuthority()
	a.CommandDigest = strings.Repeat("f", 64)
	if AgentConfigScanAuthority(a) == nil {
		t.Fatal("forged digest")
	}
}
func TestAgentConfigScanResultBindsAuthorityAndOutcome(t *testing.T) {
	a := scanAuthority()
	scan := v1.AgentConfigDiscoveryEvent{Kind: v1.EventKindAgentConfigDiscovery, ScanID: "scan-1", ProjectID: a.ProjectID, RepositoryID: a.RepositoryID, BranchRef: a.BranchRef, CommitSHA: strings.Repeat("a", 40), CatalogVersion: v1.AgentConfigCatalogVersion, ParserVersion: "runner-scanner-v1", ObservedAt: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC), Items: []v1.AgentConfigDiscoveryItem{}}
	event := v1.AgentConfigScanResultEvent{Kind: v1.EventAgentConfigScanResult, AgentConfigScanAuthority: a, CommandMessageID: v1.AgentConfigScanCommandMessageID(a.RequestID), ObservedAt: "2026-09-12T12:00:00Z", Outcome: v1.AgentConfigScanOutcome{Kind: "discovered", Scan: &scan}}
	if err := AgentConfigScanResultEvent(event); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*v1.AgentConfigScanResultEvent){
		"kind":         func(e *v1.AgentConfigScanResultEvent) { e.Kind = "other" },
		"command":      func(e *v1.AgentConfigScanResultEvent) { e.CommandMessageID = "command-1" },
		"late":         func(e *v1.AgentConfigScanResultEvent) { e.ObservedAt = "2026-09-12T12:11:00Z" },
		"early":        func(e *v1.AgentConfigScanResultEvent) { e.ObservedAt = "2026-09-12T11:59:00Z" },
		"missing scan": func(e *v1.AgentConfigScanResultEvent) { e.Outcome.Scan = nil },
		"mixed":        func(e *v1.AgentConfigScanResultEvent) { e.Outcome.Code = "scan_failed" },
		"scope": func(e *v1.AgentConfigScanResultEvent) {
			s := *e.Outcome.Scan
			s.ProjectID = "other"
			e.Outcome.Scan = &s
		},
		"unknown outcome": func(e *v1.AgentConfigScanResultEvent) { e.Outcome.Kind = "partial" },
	} {
		t.Run(name, func(t *testing.T) {
			e := event
			change(&e)
			if AgentConfigScanResultEvent(e) == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
	for _, code := range []string{"repository_unavailable", "checkout_failed", "scan_failed", "deadline_exceeded", "interrupted", "stopped", "limit_exceeded"} {
		e := event
		e.Outcome = v1.AgentConfigScanOutcome{Kind: "failed", Code: code}
		if err := AgentConfigScanResultEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	event.Outcome = v1.AgentConfigScanOutcome{Kind: "failed", Code: "secret diagnostics"}
	if AgentConfigScanResultEvent(event) == nil {
		t.Fatal("unknown failure accepted")
	}
}

func TestAgentConfigScanMatchesControlPlaneFixtures(t *testing.T) {
	for _, file := range []string{"agent_config_scan_v1_command.json", "agent_config_scan_v1_result.json", "agent_config_scan_v1_invalid_scope.json"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("../../api/runner/v1/testdata/" + file)
			if err != nil {
				t.Fatal(err)
			}
			var env v1.Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(env.Payload)
			decoder := json.NewDecoder(bytes.NewReader(payload))
			decoder.DisallowUnknownFields()
			if file == "agent_config_scan_v1_command.json" {
				var command v1.CommandPayload
				if err := decoder.Decode(&command); err != nil {
					t.Fatal(err)
				}
				if command.Command != v1.CmdAgentConfigScan || command.AgentConfigScan == nil {
					t.Fatal("wrong command")
				}
				if err := AgentConfigScanAuthority(*command.AgentConfigScan); err != nil {
					t.Fatal(err)
				}
				if env.ID != v1.AgentConfigScanCommandMessageID(command.AgentConfigScan.RequestID) {
					t.Fatal("command envelope mismatch")
				}
				env.Payload = command
			} else {
				var result v1.AgentConfigScanResultEvent
				if err := decoder.Decode(&result); err != nil {
					t.Fatal(err)
				}
				err := AgentConfigScanResultEvent(result)
				if strings.Contains(file, "invalid") {
					if err == nil {
						t.Fatal("foreign repository fixture accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if env.ID != v1.AgentConfigScanResultEventID(result.CommandMessageID) {
					t.Fatal("result envelope mismatch")
				}
				env.Payload = result
			}
			roundtrip, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			_ = json.Unmarshal(raw, &before)
			_ = json.Unmarshal(roundtrip, &after)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("wire roundtrip differs: %s", roundtrip)
			}
		})
	}
}
