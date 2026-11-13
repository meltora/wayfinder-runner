package validation

import (
	"fmt"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func RunGuardrailsPolicy(value v1.RunGuardrailsPolicy) error {
	if value.SchemaVersion != v1.RunGuardrailsSchemaVersion {
		return fmt.Errorf("run_guardrails schema_version is invalid")
	}
	if value.MaxRefusedRequests < 1 || value.MaxRefusedRequests > v1.MaxRefusedRequests {
		return fmt.Errorf("run_guardrails max_refused_requests is out of range")
	}
	return nil
}

func RefusedRequestEvent(value v1.RefusedRequestEvent) error {
	if value.Kind != v1.EventKindRefusedRequest {
		return fmt.Errorf("refused request kind is invalid")
	}
	if err := manifestBoundAgentEvent(value.ManifestKind, value.LeaseID, value.JobID, value.AttemptID,
		value.ManifestID, value.ManifestDigest, value.FencingToken, value.ObservedAt, value.AgentRunAuthority); err != nil {
		return err
	}
	if value.Sequence < 1 || value.Sequence > v1.MaxRefusedRequests {
		return fmt.Errorf("refused request sequence is out of range")
	}
	switch value.RefusalKind {
	case v1.RefusalNetworkDestination, v1.RefusalCommand, v1.RefusalTool:
	default:
		return fmt.Errorf("refused request kind is invalid")
	}
	if value.Enforcer != v1.RefusalEnforcerAgentRuntimePermissions {
		return fmt.Errorf("refused request enforcer is invalid")
	}
	if err := boundedText("subject", value.Subject, v1.MaxRefusedRequestSubjectBytes, true); err != nil || strings.TrimSpace(value.Subject) == "" {
		return fmt.Errorf("refused request subject is invalid")
	}
	if err := agentActivityRedaction(value.Redaction); err != nil {
		return fmt.Errorf("refused request redaction metadata is invalid")
	}
	if err := SHA256Digest("content_digest_sha256", value.ContentDigestSHA256); err != nil {
		return err
	}
	if value.ContentDigestSHA256 != v1.RefusedRequestContentDigest(value) {
		return fmt.Errorf("content_digest_sha256 does not bind the refused request")
	}
	return nil
}

func RunNetworkPolicyEvent(value v1.RunNetworkPolicyEvent) error {
	if value.Kind != v1.EventKindRunNetworkPolicy {
		return fmt.Errorf("run network policy kind is invalid")
	}
	if err := manifestBoundAgentEvent(value.ManifestKind, value.LeaseID, value.JobID, value.AttemptID,
		value.ManifestID, value.ManifestDigest, value.FencingToken, value.ObservedAt, value.AgentRunAuthority); err != nil {
		return err
	}
	if err := runNetworkPolicyShape(value); err != nil {
		return err
	}
	if err := SHA256Digest("content_digest_sha256", value.ContentDigestSHA256); err != nil {
		return err
	}
	if value.ContentDigestSHA256 != v1.RunNetworkPolicyContentDigest(value) {
		return fmt.Errorf("content_digest_sha256 does not bind the run network policy")
	}
	return nil
}

func runNetworkPolicyShape(value v1.RunNetworkPolicyEvent) error {
	switch value.AgentSandboxNetwork {
	case v1.AgentSandboxNetworkDenyAll, v1.AgentSandboxNetworkNotRecorded:
	default:
		return fmt.Errorf("run network policy agent_sandbox_network is invalid")
	}
	switch value.Isolation {
	case v1.RunIsolationDirect:
		if value.JobNetwork != v1.RunJobNetworkHost || value.EgressPolicyID != nil {
			return fmt.Errorf("direct isolation requires the host job network without an egress policy")
		}
		return nil
	case v1.RunIsolationDocker:
	default:
		return fmt.Errorf("run network policy isolation is invalid")
	}
	switch value.JobNetwork {
	case v1.RunJobNetworkNone:
		if value.EgressPolicyID != nil {
			return fmt.Errorf("job network none must not name an egress policy")
		}
		return nil
	case v1.RunJobNetworkRestricted:
		if value.EgressPolicyID == nil {
			return fmt.Errorf("restricted job network requires an egress policy")
		}
		return ID("egress_policy_id", *value.EgressPolicyID)
	default:
		return fmt.Errorf("docker isolation requires job network none or restricted")
	}
}
