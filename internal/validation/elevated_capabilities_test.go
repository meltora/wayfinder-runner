package validation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validElevatedCapabilities() v1.ElevatedCapabilityGrants {
	expiry := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	return v1.ElevatedCapabilityGrants{SchemaVersion: 1, Grants: []v1.ElevatedCapabilityGrant{
		{RequestID: "ecr_hook", Kind: v1.ElevatedCapabilityHook, Key: ".claude/hooks/format.json", DeclarationDigest: strings.Repeat("a", 64), ExpiresAt: expiry},
		{RequestID: "ecr_mcp", Kind: v1.ElevatedCapabilityMCPServer, Key: ".mcp.json", DeclarationDigest: strings.Repeat("b", 64), ExpiresAt: expiry},
		{RequestID: "ecr_net", Kind: v1.ElevatedCapabilityNetworkAccess, Key: "registry.npmjs.org", DeclarationDigest: strings.Repeat("c", 64), ExpiresAt: expiry},
	}}
}

func TestElevatedCapabilitiesValidation(t *testing.T) {
	if err := ElevatedCapabilities(validElevatedCapabilities()); err != nil {
		t.Fatalf("valid grants rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.ElevatedCapabilityGrants){
		"schema":         func(value *v1.ElevatedCapabilityGrants) { value.SchemaVersion = 2 },
		"empty":          func(value *v1.ElevatedCapabilityGrants) { value.Grants = nil },
		"duplicate id":   func(value *v1.ElevatedCapabilityGrants) { value.Grants[1].RequestID = value.Grants[0].RequestID },
		"request id":     func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].RequestID = "../x" },
		"kind":           func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].Kind = "plugin" },
		"empty key":      func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].Key = "" },
		"long key":       func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].Key = strings.Repeat("k", 301) },
		"control key":    func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].Key = "a\tb" },
		"padded key":     func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].Key = " .mcp.json" },
		"digest":         func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].DeclarationDigest = strings.Repeat("A", 64) },
		"missing expiry": func(value *v1.ElevatedCapabilityGrants) { value.Grants[0].ExpiresAt = time.Time{} },
		"too many": func(value *v1.ElevatedCapabilityGrants) {
			grants := make([]v1.ElevatedCapabilityGrant, v1.MaxElevatedCapabilityGrants+1)
			for index := range grants {
				grants[index] = value.Grants[0]
				grants[index].RequestID = "ecr_" + strings.Repeat("x", index+1)
			}
			value.Grants = grants
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validElevatedCapabilities()
			candidate.Grants = append([]v1.ElevatedCapabilityGrant(nil), candidate.Grants...)
			mutate(&candidate)
			if err := ElevatedCapabilities(candidate); err == nil {
				t.Fatal("invalid grants accepted")
			}
		})
	}
}

func TestJobCarriesValidatedElevatedCapabilities(t *testing.T) {
	job := validJob()
	grants := validElevatedCapabilities()
	job.ElevatedCapabilities = &grants
	if err := JobSpec(job); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
	grants.Grants[0].Kind = "plugin"
	if err := JobSpec(job); err == nil {
		t.Fatal("job with invalid elevated capabilities accepted")
	}
}

func TestJobSpecDecodesElevatedCapabilitiesStrictly(t *testing.T) {
	raw := []byte(`{"job_id":"j","attempt_id":"a","project_id":"p","kind":"build","lease_id":"l","manifest_id":"m","manifest_digest":"d","workflow_revision":1,"fencing_token":1,"lease_expires_at":"2027-01-01T00:00:00Z","repositories":[],"ticket":{"key":"K","summary":"s","description":"d"},"elevated_capabilities":{"schema_version":1,"grants":[{"request_id":"ecr_1","kind":"mcp_server","key":".mcp.json","declaration_digest":"` + strings.Repeat("a", 64) + `","expires_at":"2027-01-01T00:00:00Z"}]}}`)
	var job v1.JobSpec
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatalf("decode job with grants: %v", err)
	}
	if job.ElevatedCapabilities == nil || len(job.ElevatedCapabilities.Grants) != 1 || job.ElevatedCapabilities.Grants[0].Kind != v1.ElevatedCapabilityMCPServer {
		t.Fatalf("grants not decoded: %+v", job.ElevatedCapabilities)
	}
}
