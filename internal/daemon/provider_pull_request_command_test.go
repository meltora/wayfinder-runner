package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakeProviderPullRequestRunner struct {
	calls   atomic.Int32
	delay   time.Duration
	outcome v1.ProviderPullRequestResultOutcome
}

func (f *fakeProviderPullRequestRunner) ReadyProviderPullRequestInstallations() []v1.ProviderPullRequestInstallationCapability {
	return []v1.ProviderPullRequestInstallationCapability{{InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com"}}
}

func (f *fakeProviderPullRequestRunner) RunProviderPullRequest(context.Context, v1.ProviderPullRequestAuthority, *v1.ProviderCredential) v1.ProviderPullRequestResultOutcome {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.outcome
}

func validProviderPullRequestCommand(operation string, lease time.Time) v1.CommandPayload {
	id := "22345678-1234-4234-8234-123456789abc"
	a := v1.ProviderPullRequestAuthority{AuthorityVersion: v1.ProviderPullRequestTransportVersion, OperationID: id, Operation: operation, OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1", RepositoryID: "repository-1", Provider: "github", InstallationID: "installation-1", ProviderInstanceKey: "github.com", ProviderRepositoryID: "1385038985", PullRequestNumber: "2", ExpectedHeadSHA: strings.Repeat("e", 40), LeaseID: v1.ProviderPullRequestLeaseID(id), FencingToken: 1, LeaseExpiresAt: v1.ProviderPullRequestTime(lease)}
	command := v1.CmdProviderPullRequestObservation
	if operation == v1.ProviderPullRequestOperationMerge {
		method := "squash"
		a.MergeMethod = &method
		command = v1.CmdProviderPullRequestMerge
	}
	a.CommandDigest = v1.ProviderPullRequestCommandDigest(a)
	return v1.CommandPayload{Command: command, ProviderPullRequest: &a}
}

func TestProviderPullRequestMergeRunsOnceInSeparateLane(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderPullRequestCommand(v1.ProviderPullRequestOperationMerge, time.Now().Add(10*time.Minute))
	id := v1.ProviderPullRequestCommandMessageID(command.ProviderPullRequest.OperationID)
	if reason := commandRejectionReason(jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command)); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason=%s", reason)
	}
	sha := strings.Repeat("a", 40)
	fake := &fakeProviderPullRequestRunner{outcome: v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeMerged, MergeCommitSHA: &sha}}
	jr.d.providerPullRequestRunner = fake
	var events atomic.Int32
	var event v1.ProviderPullRequestResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderPullRequestResultEvent)
		events.Add(1)
		return nil
	}
	envelope := v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: id, SentAt: time.Now(), Payload: command}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerPullRequestCommands.Wait()
	envelope.SentAt = time.Now()
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerPullRequestCommands.Wait()
	if fake.calls.Load() != 1 || events.Load() != 1 {
		t.Fatalf("calls=%d events=%d", fake.calls.Load(), events.Load())
	}
	if event.Outcome.Kind != v1.ProviderPullRequestOutcomeMerged || event.CommandMessageID != id {
		t.Fatalf("event=%+v", event)
	}
}

func TestProviderPullRequestObservationLeaseCrossingFailsClosed(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderPullRequestCommand(v1.ProviderPullRequestOperationObserve, time.Now().Add(1500*time.Millisecond))
	fake := &fakeProviderPullRequestRunner{delay: 1600 * time.Millisecond, outcome: v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeObserved, State: "open", HeadSHA: command.ProviderPullRequest.ExpectedHeadSHA}}
	jr.d.providerPullRequestRunner = fake
	var event v1.ProviderPullRequestResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderPullRequestResultEvent)
		return nil
	}
	id := v1.ProviderPullRequestCommandMessageID(command.ProviderPullRequest.OperationID)
	if err := jr.d.executeProviderPullRequest(context.Background(), id, *command.ProviderPullRequest, nil); err != nil {
		t.Fatal(err)
	}
	if event.Outcome.Kind != v1.ProviderPullRequestOutcomeFailed || event.Outcome.Code != v1.ProviderPullRequestDeadlineExceeded || !event.ObservedAt.Equal(command.ProviderPullRequest.LeaseExpiresAt) {
		t.Fatalf("event=%+v", event)
	}
}

func TestProviderPullRequestBoundaryRejectsSmugglingAndDrift(t *testing.T) {
	command := validProviderPullRequestCommand(v1.ProviderPullRequestOperationMerge, time.Now().Add(10*time.Minute))
	if err := validateProviderPullRequestCommandBoundary(command); err != nil {
		t.Fatal(err)
	}
	smuggled := command
	smuggled.JobID = "job-1"
	drift := command
	drift.Command = v1.CmdProviderPullRequestObservation
	foreign := validProviderReviewFeedbackContentCommand()
	foreign.ProviderPullRequest = command.ProviderPullRequest
	for name, c := range map[string]v1.CommandPayload{"smuggled": smuggled, "drift": drift, "foreign": foreign} {
		if reason := commandRejectionReason(validateProviderPullRequestCommandBoundary(c)); reason != v1.CommandAckReasonInvalidCommand {
			t.Fatalf("%s reason=%s", name, reason)
		}
	}
	expired := validProviderPullRequestCommand(v1.ProviderPullRequestOperationMerge, time.Now().Add(-time.Minute))
	jr, _ := testJobRunner(t)
	jr.d.providerPullRequestRunner = &fakeProviderPullRequestRunner{}
	if reason := commandRejectionReason(jr.d.executeProviderPullRequest(context.Background(), v1.ProviderPullRequestCommandMessageID(expired.ProviderPullRequest.OperationID), *expired.ProviderPullRequest, nil)); reason != v1.CommandAckReasonLeaseRejected {
		t.Fatalf("reason=%s", reason)
	}
}
