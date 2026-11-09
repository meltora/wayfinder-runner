package daemon

import (
	"context"
	"sort"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/hookspool"
	"github.com/example/app-runner/internal/toolsummary"
	"github.com/example/app-runner/internal/validation"
)

const (
	runnerRedactionPolicyVersion = "runner-redaction-2"
	maxSanitizedLogsPerJob       = 512
)

func (jr *jobRunner) summarizeProviderTools(providerEvent map[string]any) *toolsummary.Summary {
	if jr.d.cfg.Provider != "claude" {
		return nil
	}
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	if jr.toolTracker == nil {
		jr.toolTracker = toolsummary.NewTracker()
	}
	summary, ok := toolsummary.Summarize(providerEvent, jr.toolTracker)
	if !ok {
		return nil
	}
	return &summary
}

func (jr *jobRunner) publishSanitizedAgentStatus(providerEvent map[string]any, tools *toolsummary.Summary) {
	if jr.job.AgentRunAuthority == nil || !jr.startEvidenceRecorded() ||
		jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken) != nil {
		return
	}
	jr.procMu.Lock()
	jr.sanitizedLogs++
	sequence := jr.sanitizedLogs
	jr.procMu.Unlock()
	if sequence > maxSanitizedLogsPerJob+1 {
		return
	}

	summary := "Agent emitted a structured status event."
	source := v1.SanitizedLogAgentStatus
	detail := ""
	truncated := false
	redactionCount := 0
	categories := map[v1.SanitizedLogRedactionCategory]struct{}{}
	if sequence == maxSanitizedLogsPerJob+1 {
		summary = "Additional agent status entries were suppressed."
		truncated = true
		redactionCount = 1
		categories[v1.SanitizedLogToolOutput] = struct{}{}
	} else {
		eventType, eventTypeCategory, eventTypeTruncated := sanitizeSelectedLogValue(providerEvent["type"], 128, allowedAgentEventType)
		if eventType != "" {
			summary = "Agent status: " + eventType
		}
		if eventTypeCategory != "" {
			redactionCount++
			categories[eventTypeCategory] = struct{}{}
		}
		truncated = truncated || eventTypeTruncated
		status, statusCategory, statusTruncated := sanitizeSelectedLogValue(providerEvent["status"], 256, allowedAgentStatus)
		if status != "" {
			detail = "Status: " + status
		}
		if statusCategory != "" {
			redactionCount++
			categories[statusCategory] = struct{}{}
		}
		truncated = truncated || statusTruncated
		for key, value := range providerEvent {
			if key == "type" || key == "status" {
				continue
			}
			redactionCount++
			categories[classifySuppressedProviderField(key)] = struct{}{}
			if stringContainsSecret(value) {
				categories[v1.SanitizedLogCredential] = struct{}{}
			}
		}
		if tools != nil {
			source, summary, detail = v1.SanitizedLogToolSummary, tools.Text, ""
			truncated = truncated || tools.Truncated
			if tools.CommandArgumentRedactions > 0 {
				redactionCount += tools.CommandArgumentRedactions
				categories[v1.SanitizedLogCommandArgument] = struct{}{}
			}
			if tools.ToolOutputRedactions > 0 {
				redactionCount += tools.ToolOutputRedactions
				categories[v1.SanitizedLogToolOutput] = struct{}{}
			}
		}
	}

	categoryValues := make([]string, 0, len(categories))
	for category := range categories {
		categoryValues = append(categoryValues, string(category))
	}
	sort.Strings(categoryValues)
	authority := *jr.job.AgentRunAuthority
	event := v1.SanitizedLogEvent{
		Kind: "sanitized_log", ManifestKind: jr.job.Kind, LeaseID: jr.job.LeaseID,
		JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, ManifestID: jr.job.ManifestID,
		ManifestDigest: jr.job.ManifestDigest, FencingToken: jr.job.FencingToken, ObservedAt: time.Now().UTC(),
		AgentRunAuthority: authority, Source: source,
		Summary: summary, Detail: detail, Truncated: truncated,
		Redaction: v1.SanitizedLogRedaction{
			SchemaVersion: 1, PolicyVersion: runnerRedactionPolicyVersion, Collector: "runner",
			RedactionCount: redactionCount, Categories: categoryValues,
		},
	}
	event.ContentDigestSHA256 = v1.SanitizedLogContentDigest(event)
	if err := validation.SanitizedLogEvent(event); err != nil {
		jr.logger.Warn("reject invalid sanitized agent status", "err", err)
		return
	}
	eventCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := jr.d.sendEvent(eventCtx, event); err != nil {
		jr.logger.Warn("publish sanitized agent status", "err", err)
	}
}

func sanitizeSelectedLogValue(value any, limit int, allow func(string) bool) (string, v1.SanitizedLogRedactionCategory, bool) {
	text, ok := value.(string)
	if !ok {
		return "", "", false
	}
	redacted := hookspool.Redact(map[string]any{"value": text})["value"]
	text, _ = redacted.(string)
	if text == "[redacted]" {
		return "", v1.SanitizedLogCredential, false
	}
	text = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(text))
	truncated := len(text) > limit
	if text == "" {
		return "", "", truncated
	}
	if !allow(text) {
		return "", v1.SanitizedLogToolOutput, truncated
	}
	return truncateUTF8Bytes(text, limit), "", truncated
}

func allowedAgentEventType(value string) bool {
	switch value {
	case "assistant", "error", "event", "local", "result", "status", "system", "text", "tool", "tool_result", "tool_use", "user":
		return true
	default:
		return false
	}
}

func allowedAgentStatus(value string) bool {
	switch value {
	case "cancelled", "complete", "completed", "error", "failed", "failure", "in_progress", "ok", "pending", "running", "started", "success":
		return true
	default:
		return false
	}
}

func classifySuppressedProviderField(key string) v1.SanitizedLogRedactionCategory {
	normalized := strings.ToLower(key)
	switch {
	case strings.Contains(normalized, "prompt"):
		return v1.SanitizedLogPrompt
	case strings.Contains(normalized, "credential") || strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") || strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "authorization") || strings.Contains(normalized, "cookie"):
		return v1.SanitizedLogCredential
	case normalized == "env" || strings.Contains(normalized, "environment"):
		return v1.SanitizedLogEnvironment
	case strings.Contains(normalized, "path") || normalized == "cwd" || strings.Contains(normalized, "file"):
		return v1.SanitizedLogFilesystemPath
	case strings.Contains(normalized, "email") || strings.Contains(normalized, "user") || strings.Contains(normalized, "name"):
		return v1.SanitizedLogPersonalData
	case strings.Contains(normalized, "command") || strings.Contains(normalized, "arg"):
		return v1.SanitizedLogCommandArgument
	default:
		return v1.SanitizedLogToolOutput
	}
}

func stringContainsSecret(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	redacted, _ := hookspool.Redact(map[string]any{"value": text})["value"].(string)
	return redacted == "[redacted]"
}
