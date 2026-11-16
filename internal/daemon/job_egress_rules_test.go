package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/validation"
)

type fakeJobEgressRulesLease struct {
	failures    chan error
	completeErr error
	completed   int
	closed      int
}

func newFakeJobEgressRulesLease() *fakeJobEgressRulesLease {
	return &fakeJobEgressRulesLease{failures: make(chan error, 1)}
}

func (lease *fakeJobEgressRulesLease) Failures() <-chan error { return lease.failures }
func (lease *fakeJobEgressRulesLease) Complete(context.Context) error {
	lease.completed++
	return lease.completeErr
}
func (lease *fakeJobEgressRulesLease) Close() error {
	lease.closed++
	return nil
}

type recordedJobRulesAcquirer struct {
	requests []egresspolicy.JobRulesRequest
	lease    *fakeJobEgressRulesLease
	err      error
}

func (recorder *recordedJobRulesAcquirer) acquire(_ context.Context, request egresspolicy.JobRulesRequest) (jobEgressRulesLease, egresspolicy.JobRules, error) {
	recorder.requests = append(recorder.requests, request)
	if recorder.err != nil {
		return nil, egresspolicy.JobRules{}, recorder.err
	}
	return recorder.lease, egresspolicy.JobRules{
		Binding: egresspolicy.Binding{
			NetworkID: request.NetworkID, PolicyID: request.PolicyID,
			EnforcementID: "nft:runner-egress-7", RulesDigest: strings.Repeat("e", 64),
		},
		ApprovedHosts: request.ApprovedHosts, ApprovedHostsDigest: v1.EgressApprovedHostsDigest(request.ApprovedHosts),
	}, nil
}

func restrictedEgressJobRunner(t *testing.T, schemaVersion int) (*jobRunner, *capturedEvents, *recordedJobRulesAcquirer) {
	t.Helper()
	jr, events := guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: schemaVersion, MaxRefusedRequests: 10})
	jr.d.runnerID = "runner-1"
	jr.d.cfg.JobNetwork, jr.d.cfg.JobNetworkName, jr.d.cfg.EgressPolicyID = "restricted", "runner-egress", "egress-dev"
	jr.d.jobNetworkAttestation = isolation.DockerNetworkAttestation{
		ID: strings.Repeat("b", 64), Name: "runner-egress", PolicyID: "egress-dev", Driver: "bridge", Scope: "local",
	}
	recorder := &recordedJobRulesAcquirer{lease: newFakeJobEgressRulesLease()}
	jr.d.acquireJobEgressRules = recorder.acquire
	return jr, events, recorder
}

func publishedRunNetworkPolicy(t *testing.T, jr *jobRunner, events *capturedEvents) v1.RunNetworkPolicyEvent {
	t.Helper()
	jr.publishRunNetworkPolicy(context.Background())
	published := events.byKind(v1.EventKindRunNetworkPolicy)
	if len(published) != 1 {
		t.Fatalf("published=%v", published)
	}
	raw, err := json.Marshal(published[0])
	if err != nil {
		t.Fatal(err)
	}
	var event v1.RunNetworkPolicyEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if err := validation.RunNetworkPolicyEvent(event); err != nil {
		t.Fatalf("published invalid event: %v", err)
	}
	return event
}

func TestJobEgressRulesApplyApprovedHostsToTheJobContainerAndReportTheVerifierDigest(t *testing.T) {
	jr, events, recorder := restrictedEgressJobRunner(t, 2)
	jr.acquireJobEgressRules(context.Background(), []string{"*.npmjs.org", "registry.example.com"})
	defer jr.closeJobEgressRules()
	if len(recorder.requests) != 1 {
		t.Fatalf("requests=%v", recorder.requests)
	}
	request := recorder.requests[0]
	if request.NetworkID != strings.Repeat("b", 64) || request.NetworkName != "runner-egress" || request.PolicyID != "egress-dev" ||
		request.ContainerName != isolation.ContainerName("runner-1", jr.job.AttemptID) ||
		!reflect.DeepEqual(request.ApprovedHosts, []string{"*.npmjs.org", "registry.example.com"}) {
		t.Fatalf("request=%+v", request)
	}
	event := publishedRunNetworkPolicy(t, jr, events)
	if event.EgressRules == nil || event.EgressRules.RulesDigest != strings.Repeat("e", 64) ||
		event.EgressRules.EnforcementID != "nft:runner-egress-7" ||
		!reflect.DeepEqual(event.EgressRules.ApprovedHosts, request.ApprovedHosts) {
		t.Fatalf("egress rules=%+v", event.EgressRules)
	}
	if err := jr.releaseJobEgressRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder.lease.completed != 1 || recorder.lease.closed != 1 {
		t.Fatalf("lease completed=%d closed=%d", recorder.lease.completed, recorder.lease.closed)
	}
}

func TestJobEgressRulesAreHeldButNotReportedToAnAppThatDidNotAskForThem(t *testing.T) {
	jr, events, recorder := restrictedEgressJobRunner(t, 1)
	jr.acquireJobEgressRules(context.Background(), nil)
	defer jr.closeJobEgressRules()
	if len(recorder.requests) != 1 || recorder.requests[0].ApprovedHosts == nil {
		t.Fatalf("requests=%+v", recorder.requests)
	}
	if event := publishedRunNetworkPolicy(t, jr, events); event.EgressRules != nil {
		t.Fatalf("schema version 1 event carries egress rules: %+v", event.EgressRules)
	}
	raw, _ := json.Marshal(events.byKind(v1.EventKindRunNetworkPolicy)[0])
	if strings.Contains(string(raw), "egress_rules") {
		t.Fatalf("schema version 1 wire event carries egress rules: %s", raw)
	}
}

func TestJobEgressRulesUnavailableLeaveApprovedHostsDeniedAndUnreported(t *testing.T) {
	jr, events, recorder := restrictedEgressJobRunner(t, 2)
	recorder.err = errors.New("verifier socket missing")
	jr.acquireJobEgressRules(context.Background(), []string{"registry.example.com"})
	if event := publishedRunNetworkPolicy(t, jr, events); event.EgressRules != nil {
		t.Fatalf("unverified rules reported: %+v", event.EgressRules)
	}
	if err := jr.releaseJobEgressRules(context.Background()); err != nil {
		t.Fatalf("release without held rules failed: %v", err)
	}
}

func TestJobEgressRulesAreOnlyRequestedForRestrictedDockerNetworks(t *testing.T) {
	for name, configure := range map[string]func(*jobRunner){
		"network none": func(jr *jobRunner) { jr.d.cfg.JobNetwork = "none" },
		"direct":       func(jr *jobRunner) { jr.d.cfg.Isolation = "direct" },
		"unattested":   func(jr *jobRunner) { jr.d.jobNetworkAttestation = isolation.DockerNetworkAttestation{} },
	} {
		t.Run(name, func(t *testing.T) {
			jr, _, recorder := restrictedEgressJobRunner(t, 2)
			configure(jr)
			jr.acquireJobEgressRules(context.Background(), []string{"registry.example.com"})
			if len(recorder.requests) != 0 || jr.egressRules != nil {
				t.Fatalf("requests=%v", recorder.requests)
			}
		})
	}
}

func TestJobEgressRulesLostDuringTheRunStopTheProviderAndFailRelease(t *testing.T) {
	jr, events, recorder := restrictedEgressJobRunner(t, 2)
	process := &recordingProcess{}
	jr.procMu.Lock()
	jr.proc = process
	jr.procMu.Unlock()
	jr.acquireJobEgressRules(context.Background(), []string{"registry.example.com"})
	recorder.lease.failures <- errors.New("verifier disconnected")
	deadline := time.Now().Add(time.Second)
	for process.kills() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if process.kills() == 0 {
		t.Fatal("provider kept running after the job egress rules lease ended")
	}
	if event := publishedRunNetworkPolicy(t, jr, events); event.EgressRules != nil {
		t.Fatalf("lost rules reported: %+v", event.EgressRules)
	}
	if err := jr.releaseJobEgressRules(context.Background()); !errors.Is(err, errJobEgressRulesNotHeld) {
		t.Fatalf("release err=%v", err)
	}
}

func TestJobEgressRulesReleaseWithoutHeldThroughAcknowledgementFails(t *testing.T) {
	jr, _, recorder := restrictedEgressJobRunner(t, 2)
	recorder.lease.completeErr = errors.New("invalid release acknowledgement")
	jr.acquireJobEgressRules(context.Background(), nil)
	if err := jr.releaseJobEgressRules(context.Background()); !errors.Is(err, errJobEgressRulesNotHeld) {
		t.Fatalf("release err=%v", err)
	}
	if recorder.lease.closed != 1 {
		t.Fatalf("lease closed=%d", recorder.lease.closed)
	}
}

func TestJobEgressRulesFailureBeforeProviderStartIsDetectedAtStart(t *testing.T) {
	jr, _, recorder := restrictedEgressJobRunner(t, 2)
	jr.acquireJobEgressRules(context.Background(), nil)
	defer jr.closeJobEgressRules()
	recorder.lease.failures <- errors.New("verifier disconnected")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jr.procMu.Lock()
		failed := jr.jobEgressRulesFailedLocked()
		jr.procMu.Unlock()
		if failed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job egress rules failure before provider start was not recorded")
}
