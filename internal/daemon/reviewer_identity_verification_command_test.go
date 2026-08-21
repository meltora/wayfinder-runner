package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakeReviewerIdentityVerifier struct {
	outcome v1.ReviewerIdentityVerificationResultOutcome
	calls   int
}

type blockingReviewerIdentityVerifier struct {
	started chan struct{}
	release chan struct{}
	outcome v1.ReviewerIdentityVerificationResultOutcome
}

func (verifier *blockingReviewerIdentityVerifier) ReadyReviewerIdentityInstallations() []v1.ReviewerIdentityVerificationInstallationCapability {
	return []v1.ReviewerIdentityVerificationInstallationCapability{
		{InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com"},
	}
}

func (verifier *blockingReviewerIdentityVerifier) VerifyReviewerIdentity(
	ctx context.Context,
	_ v1.ReviewerIdentityVerificationOperation,
) v1.ReviewerIdentityVerificationResultOutcome {
	close(verifier.started)
	select {
	case <-verifier.release:
		return verifier.outcome
	case <-ctx.Done():
		return v1.ReviewerIdentityVerificationResultOutcome{
			Kind: v1.ReviewerIdentityVerificationRetryable,
			Code: v1.ReviewerIdentityProviderUnavailable,
		}
	}
}

func (verifier *fakeReviewerIdentityVerifier) ReadyReviewerIdentityInstallations() []v1.ReviewerIdentityVerificationInstallationCapability {
	return []v1.ReviewerIdentityVerificationInstallationCapability{
		{InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com"},
	}
}

func (verifier *fakeReviewerIdentityVerifier) VerifyReviewerIdentity(
	_ context.Context,
	_ v1.ReviewerIdentityVerificationOperation,
) v1.ReviewerIdentityVerificationResultOutcome {
	verifier.calls++
	return verifier.outcome
}

func validReviewerIdentityVerificationCommand() v1.CommandPayload {
	repositories := []v1.ReviewerIdentityVerificationRepository{
		{RepositoryID: "repository-1", RepositoryExternalID: "101"},
	}
	return v1.CommandPayload{
		Command: v1.CmdReviewerIdentityVerification,
		ReviewerIdentityVerification: &v1.ReviewerIdentityVerificationOperation{
			SchemaVersion:                           v1.ReviewerIdentityVerificationSchemaVersion,
			ReviewerIdentityVerificationOperationID: "reviewer-identity-verification-operation-" + strings.Repeat("a", 64),
			OrganizationID:                          "organization-1", InstallationID: "installation-1",
			InstallationStateRevision: 2, InstallationAuthorityRevision: 3,
			InstallationAuthorityDigest: strings.Repeat("b", 64),
			Provider:                    "github", ProviderInstanceKey: "github.com",
			ProviderAccountProofID: "account-proof-1", ProviderAccountProofRevision: 4,
			ProviderAccountProofDigest: strings.Repeat("c", 64), ProviderAccountID: "42",
			RepositorySetDigest: v1.ReviewerIdentityRepositorySetDigest(repositories),
			OperationDigest:     strings.Repeat("d", 64), CommandDigest: strings.Repeat("e", 64),
			LeaseID: "identity-lease-1", FencingToken: 5,
			LeaseExpiresAt: time.Now().Add(time.Minute), Repositories: repositories,
		},
	}
}

func TestReviewerIdentityVerificationCommandRemainsDisabledUntilExactInstallationCapabilityExists(t *testing.T) {
	jr, _ := testJobRunner(t)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{}, validReviewerIdentityVerificationCommand())
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason = %q, want unsupported command (err=%v)", reason, err)
	}
}

func TestReviewerIdentityVerificationCommandRejectsCrossCapabilityPayload(t *testing.T) {
	tests := map[string]func(v1.CommandPayload) v1.CommandPayload{
		"missing operation": func(command v1.CommandPayload) v1.CommandPayload {
			command.ReviewerIdentityVerification = nil
			return command
		},
		"invalid operation": func(command v1.CommandPayload) v1.CommandPayload {
			command.ReviewerIdentityVerification.FencingToken = 0
			return command
		},
		"job smuggling": func(command v1.CommandPayload) v1.CommandPayload {
			command.JobID = "smuggled-job"
			return command
		},
		"publication smuggling": func(command v1.CommandPayload) v1.CommandPayload {
			command.SourcePublication = &v1.SourcePublicationOperation{}
			return command
		},
		"payload under another command": func(command v1.CommandPayload) v1.CommandPayload {
			command.Command = v1.CmdResumeRunner
			return command
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			err := jr.d.executeCommand(context.Background(), v1.Envelope{}, mutate(validReviewerIdentityVerificationCommand()))
			if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
				t.Fatalf("reason = %q, want invalid command (err=%v)", reason, err)
			}
		})
	}
}

func TestReviewerIdentityVerificationCommandEmitsOneDurableNormalizedResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validReviewerIdentityVerificationCommand()
	verifier := &fakeReviewerIdentityVerifier{
		outcome: v1.ReviewerIdentityVerificationResultOutcome{
			Kind:             v1.ReviewerIdentityVerificationSucceeded,
			ProviderUsername: "octocat", AssignmentPrincipal: "octocat",
			Repositories: []v1.ReviewerIdentityRepositoryEvidence{
				{RepositoryID: "repository-1", RepositoryExternalID: "101", EffectiveReviewerAccess: v1.ReviewerIdentityEligible},
			},
		},
	}
	jr.d.reviewerIdentityVerifier = verifier
	var events []v1.ReviewerIdentityVerificationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event, ok := payload.(v1.ReviewerIdentityVerificationResultEvent)
		if !ok {
			t.Fatalf("event type = %T", payload)
		}
		events = append(events, event)
		return nil
	}
	envelope := v1.Envelope{ID: "reviewer-identity-verification-command-" + strings.Repeat("f", 64)}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 2 || len(events) != 1 {
		t.Fatalf("verifier calls = %d, events = %d", verifier.calls, len(events))
	}
	event := events[0]
	if event.CommandMessageID != envelope.ID || event.ReviewerIdentityVerificationOperationID !=
		command.ReviewerIdentityVerification.ReviewerIdentityVerificationOperationID ||
		event.Outcome.Kind != v1.ReviewerIdentityVerificationSucceeded || event.ObservedAt.IsZero() {
		t.Fatalf("event = %+v", event)
	}
}

func TestReviewerIdentityVerificationRejectsExpiredAuthorityBeforeProviderCall(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validReviewerIdentityVerificationCommand()
	command.ReviewerIdentityVerification.LeaseExpiresAt = time.Now().Add(-time.Second)
	verifier := &fakeReviewerIdentityVerifier{}
	jr.d.reviewerIdentityVerifier = verifier
	err := jr.d.executeCommand(context.Background(), v1.Envelope{
		ID: "reviewer-identity-verification-command-" + strings.Repeat("f", 64),
	}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonLeaseRejected {
		t.Fatalf("reason = %q, want lease rejected (err=%v)", reason, err)
	}
	if verifier.calls != 0 {
		t.Fatalf("expired authority reached provider %d times", verifier.calls)
	}
}

func TestReviewerIdentityVerificationRejectsInvalidCommandMessageIDBeforeProviderCall(t *testing.T) {
	jr, _ := testJobRunner(t)
	verifier := &fakeReviewerIdentityVerifier{}
	jr.d.reviewerIdentityVerifier = verifier
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "loose-command-id"}, validReviewerIdentityVerificationCommand())
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
		t.Fatalf("reason = %q, want invalid command (err=%v)", reason, err)
	}
	if verifier.calls != 0 {
		t.Fatalf("invalid command message ID reached provider %d times", verifier.calls)
	}
}

func TestReviewerIdentityVerificationDoesNotBlockCommandLoopAndDeduplicatesInflightDelivery(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validReviewerIdentityVerificationCommand()
	verifier := &blockingReviewerIdentityVerifier{
		started: make(chan struct{}), release: make(chan struct{}),
		outcome: v1.ReviewerIdentityVerificationResultOutcome{
			Kind:             v1.ReviewerIdentityVerificationSucceeded,
			ProviderUsername: "octocat", AssignmentPrincipal: "octocat",
			Repositories: []v1.ReviewerIdentityRepositoryEvidence{
				{RepositoryID: "repository-1", RepositoryExternalID: "101", EffectiveReviewerAccess: v1.ReviewerIdentityEligible},
			},
		},
	}
	jr.d.reviewerIdentityVerifier = verifier
	var events int
	jr.d.eventSink = func(context.Context, any) error {
		events++
		return nil
	}
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand,
		ID:     "reviewer-identity-verification-command-" + strings.Repeat("9", 64),
		SentAt: time.Now(), Payload: command,
	}
	returned := make(chan error, 1)
	go func() { returned <- jr.d.OnCommand(context.Background(), envelope) }()
	select {
	case <-verifier.started:
	case <-time.After(time.Second):
		t.Fatal("reviewer verification did not start")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reviewer verification blocked the command loop")
	}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatal("verification result emitted before provider completed")
	}
	close(verifier.release)
	jr.d.reviewerIdentityVerificationCommands.Wait()
	if events != 1 {
		t.Fatalf("events = %d", events)
	}
}
