package v1_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type runNetworkPolicyFixture struct {
	Version  int                      `json:"version"`
	Type     v1.MessageType           `json:"type"`
	ID       string                   `json:"id"`
	SentAt   time.Time                `json:"sent_at"`
	RunnerID string                   `json:"runner_id"`
	Payload  v1.RunNetworkPolicyEvent `json:"payload"`
}

func TestRunNetworkPolicyEgressRulesFixtureStrictlyParsesAndValidates(t *testing.T) {
	var fixture runNetworkPolicyFixture
	decodeStrictFixture(t, "testdata/run_network_policy_egress_rules_v1_event.json", &fixture)
	if fixture.Version != v1.ProtocolVersion || fixture.Type != v1.TypeEvent {
		t.Fatalf("fixture envelope = %+v", fixture)
	}
	if err := validation.RunNetworkPolicyEvent(fixture.Payload); err != nil {
		t.Fatal(err)
	}
	rules := fixture.Payload.EgressRules
	if rules == nil || strings.Join(rules.ApprovedHosts, ",") != "*.npmjs.org,registry.example.com" {
		t.Fatalf("egress rules = %+v", rules)
	}
}

func TestEgressApprovedHostsDigestIsSHA256OfCompactJSONArray(t *testing.T) {
	if got := v1.EgressApprovedHostsDigest(nil); got != "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945" {
		t.Fatalf("empty digest = %s", got)
	}
	if got := v1.EgressApprovedHostsDigest([]string{"*.npmjs.org", "registry.example.com"}); got != "dbd139f5f14c615f81594820e2dd5e1226ea6352555ffbbb0c91fa345ab8ecd0" {
		t.Fatalf("hosts digest = %s", got)
	}
}

func TestRunNetworkPolicyOmitsEgressRulesUnlessVerified(t *testing.T) {
	policyID := "egress-dev"
	raw, err := json.Marshal(v1.RunNetworkPolicyEvent{
		Isolation: v1.RunIsolationDocker, JobNetwork: v1.RunJobNetworkRestricted, EgressPolicyID: &policyID,
		AgentSandboxNetwork: v1.AgentSandboxNetworkDenyAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "egress_rules") {
		t.Fatalf("unverified event carries egress rules: %s", raw)
	}
}

func TestRunGuardrailsPolicyReportsEgressRulesFromSchemaVersionTwo(t *testing.T) {
	var absent *v1.RunGuardrailsPolicy
	if absent.ReportsEgressRules() || (&v1.RunGuardrailsPolicy{SchemaVersion: 1}).ReportsEgressRules() {
		t.Fatal("schema version 1 must not report egress rules")
	}
	if !(&v1.RunGuardrailsPolicy{SchemaVersion: 2}).ReportsEgressRules() {
		t.Fatal("schema version 2 must report egress rules")
	}
}
