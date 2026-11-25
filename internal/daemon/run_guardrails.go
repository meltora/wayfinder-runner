package daemon

import (
	"context"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/refusedrequest"
	"github.com/example/app-runner/internal/validation"
)

func (jr *jobRunner) publishRefusedRequests(providerEvent map[string]any) {
	policy := jr.job.RunGuardrails
	if policy == nil || jr.job.AgentRunAuthority == nil || !jr.startEvidenceRecorded() ||
		jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken) != nil {
		return
	}
	items := refusedrequest.Extract(jr.d.cfg.Provider, providerEvent)
	blocks := jr.observeNetworkBlocks(policy, providerEvent)
	if len(items) == 0 && len(blocks) == 0 {
		return
	}
	secrets := jr.knownActivitySecrets()
	for _, item := range items {
		jr.admitRefusal(policy, func(recorder *refusedrequest.Recorder) (refusedrequest.Admitted, bool) {
			return recorder.Admit(item, secrets...)
		})
	}
	for _, block := range blocks {
		jr.admitRefusal(policy, func(recorder *refusedrequest.Recorder) (refusedrequest.Admitted, bool) {
			return recorder.AdmitNetworkBlock(block, secrets...)
		})
	}
}

func (jr *jobRunner) admitRefusal(
	policy *v1.RunGuardrailsPolicy,
	admit func(*refusedrequest.Recorder) (refusedrequest.Admitted, bool),
) {
	jr.procMu.Lock()
	if jr.refusedRequests == nil {
		jr.refusedRequests = refusedrequest.NewRecorder(policy.MaxRefusedRequests)
	}
	admitted, ok := admit(jr.refusedRequests)
	jr.procMu.Unlock()
	if ok {
		jr.publishRefusedRequest(admitted)
	}
}

func (jr *jobRunner) observeNetworkBlocks(policy *v1.RunGuardrailsPolicy, providerEvent map[string]any) []refusedrequest.NetworkBlock {
	if !policy.ReportsNetworkLayerRefusals() {
		return nil
	}
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	if jr.networkBlocks == nil {
		jr.networkBlocks = refusedrequest.NewNetworkBlockDetector(jr.d.cfg.Provider, jr.networkLayersLocked())
	}
	return jr.networkBlocks.Observe(providerEvent)
}

func (jr *jobRunner) networkLayersLocked() refusedrequest.NetworkLayers {
	docker := jr.d.cfg.Isolation == "docker"
	verified := docker && jr.d.cfg.JobNetwork == "restricted" &&
		jr.egressRules != nil && jr.egressRules.rules != nil && jr.egressRules.failure == nil
	return refusedrequest.NetworkLayers{
		AgentSandbox:        docker && jr.d.cfg.Provider == "claude",
		JobEgress:           docker,
		EgressRulesVerified: verified,
		ApprovedHosts:       append([]string{}, jr.approvedNetworkHosts...),
	}
}

func (jr *jobRunner) publishRefusedRequest(admitted refusedrequest.Admitted) {
	event := v1.RefusedRequestEvent{
		Kind: v1.EventKindRefusedRequest, ManifestKind: jr.job.Kind, LeaseID: jr.job.LeaseID,
		JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, ManifestID: jr.job.ManifestID,
		ManifestDigest: jr.job.ManifestDigest, FencingToken: jr.job.FencingToken, ObservedAt: time.Now().UTC(),
		AgentRunAuthority: *jr.job.AgentRunAuthority, Sequence: admitted.Sequence, RefusalKind: admitted.Kind,
		Enforcer: admitted.Enforcer, Subject: admitted.Sanitized.Message,
		Truncated: admitted.Sanitized.Truncated,
		Redaction: v1.SanitizedLogRedaction{
			SchemaVersion: v1.RefusedRequestRedactionSchemaVersion, PolicyVersion: v1.RefusedRequestRedactionPolicyVersion,
			Collector: v1.RefusedRequestRedactionCollectorLabel, RedactionCount: admitted.Sanitized.RedactionCount,
			Categories: admitted.Sanitized.Categories,
		},
	}
	event.ContentDigestSHA256 = v1.RefusedRequestContentDigest(event)
	if err := validation.RefusedRequestEvent(event); err != nil {
		jr.logger.Warn("reject invalid refused request", "err", err)
		return
	}
	eventCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := jr.d.sendEvent(eventCtx, event); err != nil {
		jr.logger.Warn("publish refused request", "err", err)
	}
}

func (jr *jobRunner) runNetworkPolicyEvent(observedAt time.Time) v1.RunNetworkPolicyEvent {
	event := v1.RunNetworkPolicyEvent{
		Kind: v1.EventKindRunNetworkPolicy, ManifestKind: jr.job.Kind, LeaseID: jr.job.LeaseID,
		JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, ManifestID: jr.job.ManifestID,
		ManifestDigest: jr.job.ManifestDigest, FencingToken: jr.job.FencingToken, ObservedAt: observedAt.UTC(),
		AgentRunAuthority: *jr.job.AgentRunAuthority, AgentSandboxNetwork: v1.AgentSandboxNetworkNotRecorded,
	}
	switch {
	case jr.d.cfg.Isolation != "docker":
		event.Isolation, event.JobNetwork = v1.RunIsolationDirect, v1.RunJobNetworkHost
	case jr.d.cfg.JobNetwork == "restricted":
		policyID := jr.d.cfg.EgressPolicyID
		event.Isolation, event.JobNetwork, event.EgressPolicyID = v1.RunIsolationDocker, v1.RunJobNetworkRestricted, &policyID
		event.EgressRules = jr.reportedEgressRules()
	default:
		event.Isolation, event.JobNetwork = v1.RunIsolationDocker, v1.RunJobNetworkNone
	}
	if event.Isolation == v1.RunIsolationDocker && jr.d.cfg.Provider == "claude" {
		event.AgentSandboxNetwork = v1.AgentSandboxNetworkDenyAll
		if jr.previewReachOpen.Load() {
			event.AgentSandboxNetwork = v1.AgentSandboxNetworkPreviewOnly
		}
	}
	event.ContentDigestSHA256 = v1.RunNetworkPolicyContentDigest(event)
	return event
}

func (jr *jobRunner) publishRunNetworkPolicy(ctx context.Context) {
	if jr.job.RunGuardrails == nil || jr.job.AgentRunAuthority == nil || !jr.startEvidenceRecorded() {
		return
	}
	event := jr.runNetworkPolicyEvent(time.Now())
	if err := validation.RunNetworkPolicyEvent(event); err != nil {
		jr.logger.Warn("reject invalid run network policy", "err", err)
		return
	}
	eventID := "run-network-policy-" + jobStartedEventID(jr.job)[len("job-started-"):]
	if _, err := jr.d.sendEventWithStableID(ctx, eventID, event); err != nil {
		jr.logger.Warn("publish run network policy", "err", err)
	}
}
