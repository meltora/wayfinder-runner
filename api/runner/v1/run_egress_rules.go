package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	ProtocolFeatureRunNetworkEgressRules           = "run_network_egress_rules"
	ProtocolFeatureRunNetworkLayerRefusals         = "run_network_layer_refusals"
	RunGuardrailsEgressRulesSchemaVersion          = 2
	RunGuardrailsNetworkLayerRefusalsSchemaVersion = 3
	MaxEgressApprovedHosts                         = 64
)

type RunEgressRules struct {
	EnforcementID       string   `json:"enforcement_id"`
	RulesDigest         string   `json:"rules_digest"`
	ApprovedHosts       []string `json:"approved_hosts"`
	ApprovedHostsDigest string   `json:"approved_hosts_digest"`
}

func EgressApprovedHostsDigest(hosts []string) string {
	if hosts == nil {
		hosts = []string{}
	}
	raw, err := json.Marshal(hosts)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (policy *RunGuardrailsPolicy) ReportsEgressRules() bool {
	return policy != nil && policy.SchemaVersion >= RunGuardrailsEgressRulesSchemaVersion
}

func (policy *RunGuardrailsPolicy) ReportsNetworkLayerRefusals() bool {
	return policy != nil && policy.SchemaVersion >= RunGuardrailsNetworkLayerRefusalsSchemaVersion
}
