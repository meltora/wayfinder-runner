package validation

import (
	"fmt"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validEgressRules(hosts ...string) *v1.RunEgressRules {
	if hosts == nil {
		hosts = []string{}
	}
	return &v1.RunEgressRules{
		EnforcementID: "nft:runner-egress-7", RulesDigest: strings.Repeat("c", 64),
		ApprovedHosts: hosts, ApprovedHostsDigest: v1.EgressApprovedHostsDigest(hosts),
	}
}

func withEgressRules(value v1.RunNetworkPolicyEvent, rules *v1.RunEgressRules) v1.RunNetworkPolicyEvent {
	value.EgressRules = rules
	value.ContentDigestSHA256 = v1.RunNetworkPolicyContentDigest(value)
	return value
}

func TestRunNetworkPolicyEventAcceptsVerifiedEgressRulesOnlyForRestrictedNetworks(t *testing.T) {
	policyID := "egress-dev"
	restricted := validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkRestricted, &policyID)
	for name, rules := range map[string]*v1.RunEgressRules{
		"no hosts":       validEgressRules(),
		"approved hosts": validEgressRules("*.npmjs.org", "registry.example.com"),
	} {
		if err := RunNetworkPolicyEvent(withEgressRules(restricted, rules)); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	tooMany := make([]string, v1.MaxEgressApprovedHosts+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("host-%03d.example.com", index)
	}
	invalid := map[string]v1.RunNetworkPolicyEvent{
		"network none":   withEgressRules(validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkNone, nil), validEgressRules()),
		"direct":         withEgressRules(validRunNetworkPolicy(v1.RunIsolationDirect, v1.RunJobNetworkHost, nil), validEgressRules()),
		"unsorted":       withEgressRules(restricted, validEgressRules("b.example.com", "a.example.com")),
		"duplicate":      withEgressRules(restricted, validEgressRules("a.example.com", "a.example.com")),
		"uppercase":      withEgressRules(restricted, validEgressRules("A.example.com")),
		"ip literal":     withEgressRules(restricted, validEgressRules("10.0.0.1")),
		"single label":   withEgressRules(restricted, validEgressRules("localhost")),
		"bare wildcard":  withEgressRules(restricted, validEgressRules("*")),
		"too many hosts": withEgressRules(restricted, validEgressRules(tooMany...)),
	}
	nilHosts := validEgressRules()
	nilHosts.ApprovedHosts = nil
	invalid["nil hosts"] = withEgressRules(restricted, nilHosts)
	badEnforcement := validEgressRules()
	badEnforcement.EnforcementID = "bad id"
	invalid["enforcement id"] = withEgressRules(restricted, badEnforcement)
	badDigest := validEgressRules()
	badDigest.RulesDigest = "abc"
	invalid["rules digest"] = withEgressRules(restricted, badDigest)
	unbound := validEgressRules("a.example.com")
	unbound.ApprovedHostsDigest = v1.EgressApprovedHostsDigest([]string{"b.example.com"})
	invalid["hosts digest"] = withEgressRules(restricted, unbound)
	tampered := withEgressRules(restricted, validEgressRules("a.example.com"))
	tampered.EgressRules.RulesDigest = strings.Repeat("d", 64)
	invalid["content digest"] = tampered
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := RunNetworkPolicyEvent(value); err == nil {
				t.Fatal("invalid egress rules accepted")
			}
		})
	}
}
