package daemon

import (
	"context"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentactivity"
	"github.com/example/app-runner/internal/validation"
)

const (
	agentActivityTotalLimitMessage = "Activity limit reached; further agent messages are not sent."
	agentActivityRateLimitMessage  = "Agent messages are rate limited; some are skipped."
)

func (jr *jobRunner) publishAgentActivity(providerEvent map[string]any) {
	policy := jr.job.AgentActivity
	if policy == nil || jr.job.AgentRunAuthority == nil || !jr.startEvidenceRecorded() ||
		jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken) != nil {
		return
	}
	for _, item := range agentactivity.Extract(jr.d.cfg.Provider, providerEvent) {
		jr.publishAgentActivityItem(*policy, item)
	}
}

func (jr *jobRunner) knownActivitySecrets() []string {
	secrets := []string{jr.jobToken}
	if jr.providerCredential != nil {
		secrets = append(secrets, jr.providerCredential.Token)
	}
	return secrets
}

func (jr *jobRunner) publishAgentActivityItem(policy v1.AgentActivityPolicy, item agentactivity.Item) {
	sanitized := agentactivity.Sanitize(item.Text, policy.MaxMessageBytes, jr.knownActivitySecrets()...)
	if sanitized.Message == "" {
		return
	}
	jr.procMu.Lock()
	if jr.activityLimiter == nil {
		jr.activityLimiter = agentactivity.NewLimiter(policy.MaxEvents, policy.MaxEventsPerMinute)
	}
	decision := jr.activityLimiter.Admit(time.Now())
	jr.procMu.Unlock()

	activityType := item.Type
	switch decision.Action {
	case agentactivity.Drop:
		return
	case agentactivity.EmitTotalLimitNotice:
		activityType, sanitized = v1.AgentActivityLimitReached, agentactivity.Sanitized{Message: agentActivityTotalLimitMessage, Categories: []string{}}
	case agentactivity.EmitRateLimitNotice:
		activityType, sanitized = v1.AgentActivityLimitReached, agentactivity.Sanitized{Message: agentActivityRateLimitMessage, Categories: []string{}}
	}

	event := v1.AgentActivityEvent{
		Kind: v1.EventKindAgentActivity, ManifestKind: jr.job.Kind, LeaseID: jr.job.LeaseID,
		JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, ManifestID: jr.job.ManifestID,
		ManifestDigest: jr.job.ManifestDigest, FencingToken: jr.job.FencingToken, ObservedAt: time.Now().UTC(),
		AgentRunAuthority: *jr.job.AgentRunAuthority, Sequence: decision.Sequence, ActivityType: activityType,
		Message: sanitized.Message, Truncated: sanitized.Truncated,
		Redaction: v1.SanitizedLogRedaction{
			SchemaVersion: v1.AgentActivityRedactionSchemaVersion, PolicyVersion: v1.AgentActivityRedactionPolicyVersion,
			Collector: v1.AgentActivityRedactionCollectorLabel, RedactionCount: sanitized.RedactionCount,
			Categories: sanitized.Categories,
		},
	}
	event.ContentDigestSHA256 = v1.AgentActivityContentDigest(event)
	if err := validation.AgentActivityEvent(event, policy.MaxMessageBytes); err != nil {
		jr.logger.Warn("reject invalid agent activity", "err", err)
		return
	}
	eventCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := jr.d.sendEvent(eventCtx, event); err != nil {
		jr.logger.Warn("publish agent activity", "err", err)
	}
}
