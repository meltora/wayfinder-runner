package remoteconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

const MaxNetworkIdentifierLength = 128

var networkIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var reservedDockerNetworkNames = map[string]bool{"host": true, "bridge": true, "none": true, "default": true}

var networkFields = []string{"job_network", "job_network_name", "egress_policy_id"}

func parseJobNetwork(fields map[string]json.RawMessage, out *Remote) error {
	if raw, ok := fields["job_network"]; ok {
		mode, err := requiredString(raw)
		if err != nil {
			return fmt.Errorf("config job_network: %w", err)
		}
		if mode != "none" && mode != "restricted" {
			return fmt.Errorf("config job_network must be none or restricted")
		}
		out.JobNetwork = mode
	}
	rawName, hasName := fields["job_network_name"]
	rawPolicy, hasPolicy := fields["egress_policy_id"]
	if out.JobNetwork != "restricted" {
		if hasName {
			return fmt.Errorf("config job_network_name requires job_network: restricted")
		}
		if hasPolicy {
			return fmt.Errorf("config egress_policy_id requires job_network: restricted")
		}
		return nil
	}
	if !hasName {
		return fmt.Errorf("config job_network: restricted requires job_network_name")
	}
	if !hasPolicy {
		return fmt.Errorf("config job_network: restricted requires egress_policy_id")
	}
	name, err := networkIdentifier(rawName)
	if err != nil {
		return fmt.Errorf("config job_network_name: %w", err)
	}
	if reservedDockerNetworkNames[name] {
		return fmt.Errorf("config job_network_name %q is a reserved Docker network", name)
	}
	policy, err := networkIdentifier(rawPolicy)
	if err != nil {
		return fmt.Errorf("config egress_policy_id: %w", err)
	}
	out.JobNetworkName = name
	out.EgressPolicyID = policy
	return nil
}

func networkIdentifier(raw json.RawMessage) (string, error) {
	value, err := requiredString(raw)
	if err != nil {
		return "", err
	}
	if len(value) > MaxNetworkIdentifierLength || !networkIdentifierPattern.MatchString(value) {
		return "", fmt.Errorf("must be 1 to %d letters, digits, '.', '_' or '-', starting with a letter or digit", MaxNetworkIdentifierLength)
	}
	return value, nil
}

func requiredString(raw json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("must be a string, not null")
	}
	var value string
	if err := strictValue(raw, &value); err != nil {
		return "", fmt.Errorf("must be a string")
	}
	return value, nil
}
