package validation

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var agentActivityRedactionCategories = map[string]bool{
	"command_argument": true, "credential": true, "environment": true, "filesystem_path": true,
	"personal_data": true, "prompt": true, "tool_output": true,
}

func AgentActivityPolicy(value v1.AgentActivityPolicy) error {
	if value.SchemaVersion != v1.AgentActivitySchemaVersion {
		return fmt.Errorf("agent_activity schema_version is invalid")
	}
	if value.MaxEvents < 1 || value.MaxEvents > v1.MaxAgentActivityEvents {
		return fmt.Errorf("agent_activity max_events is out of range")
	}
	if value.MaxEventsPerMinute < 1 || value.MaxEventsPerMinute > v1.MaxAgentActivityEventsPerMinute {
		return fmt.Errorf("agent_activity max_events_per_minute is out of range")
	}
	if value.MaxMessageBytes < v1.MinAgentActivityMessageBytes || value.MaxMessageBytes > v1.MaxAgentActivityMessageBytes {
		return fmt.Errorf("agent_activity max_message_bytes is out of range")
	}
	return nil
}

func AgentActivityEvent(value v1.AgentActivityEvent, maxMessageBytes int) error {
	if value.Kind != v1.EventKindAgentActivity {
		return fmt.Errorf("agent activity kind is invalid")
	}
	if err := manifestBoundAgentEvent(value.ManifestKind, value.LeaseID, value.JobID, value.AttemptID,
		value.ManifestID, value.ManifestDigest, value.FencingToken, value.ObservedAt, value.AgentRunAuthority); err != nil {
		return err
	}
	if value.Sequence < 1 || value.Sequence > v1.MaxAgentActivityEvents {
		return fmt.Errorf("agent activity sequence is out of range")
	}
	switch value.ActivityType {
	case v1.AgentActivityAssistantMessage, v1.AgentActivityToolStep, v1.AgentActivityStatus, v1.AgentActivityLimitReached:
	default:
		return fmt.Errorf("agent activity type is invalid")
	}
	limit := min(maxMessageBytes, v1.MaxAgentActivityMessageBytes)
	if err := boundedText("message", value.Message, limit, true); err != nil || strings.TrimSpace(value.Message) == "" {
		return fmt.Errorf("agent activity message is invalid")
	}
	if err := agentActivityRedaction(value.Redaction); err != nil {
		return err
	}
	if err := SHA256Digest("content_digest_sha256", value.ContentDigestSHA256); err != nil {
		return err
	}
	if value.ContentDigestSHA256 != v1.AgentActivityContentDigest(value) {
		return fmt.Errorf("content_digest_sha256 does not bind the agent activity")
	}
	return nil
}

func agentActivityRedaction(value v1.SanitizedLogRedaction) error {
	policyErr := boundedText("policy_version", value.PolicyVersion, 128, true)
	if value.SchemaVersion != v1.AgentActivityRedactionSchemaVersion || value.Collector != v1.AgentActivityRedactionCollectorLabel ||
		policyErr != nil || strings.TrimSpace(value.PolicyVersion) == "" ||
		value.RedactionCount < 0 || value.RedactionCount > 1_000_000 ||
		(value.RedactionCount == 0) != (len(value.Categories) == 0) {
		return fmt.Errorf("agent activity redaction metadata is invalid")
	}
	if !sort.StringsAreSorted(value.Categories) {
		return fmt.Errorf("agent activity redaction categories must be sorted")
	}
	for index, category := range value.Categories {
		if !agentActivityRedactionCategories[category] || index > 0 && value.Categories[index-1] == category {
			return fmt.Errorf("agent activity redaction category is invalid")
		}
	}
	return nil
}
