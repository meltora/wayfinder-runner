package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/validation"
)

func TestPushCredentialAppliesOnlyToGitHubHTTPSOrigins(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.SourcePublicationInstallations = map[string]config.SourcePublicationInstallationConfig{
		"installation-github": {Provider: "github", APIBaseURL: "https://api.github.com"},
		"installation-gitlab": {Provider: "gitlab", APIBaseURL: "https://gitlab.com/api/v4", TokenFile: "/run/token"},
	}
	jr.providerCredential = testProviderCredential(daemonProviderToken)
	now := time.Now()
	for _, origin := range []string{
		"https://github.com/octo/example.git",
		"https://GitHub.com/octo/example",
	} {
		credential, err := jr.pushCredential(origin, now)
		if err != nil || credential == nil {
			t.Fatalf("%s: credential=%v err=%v", origin, credential, err)
		}
		if credential.Username != "x-access-token" || credential.Password != daemonProviderToken {
			t.Fatalf("%s: unexpected credential identity", origin)
		}
	}
	for _, origin := range []string{
		"ssh://git@github.com/octo/example.git",
		"git@github.com:octo/example.git",
		"https://gitlab.com/octo/example.git",
		"https://user@github.com/octo/example.git",
		"http://github.com/octo/example.git",
		"https://github.com:8443/octo/example.git",
		"https://api.github.com/octo/example.git",
		"https://ghe.example.com/octo/example.git",
		"/srv/git/example.git",
	} {
		credential, err := jr.pushCredential(origin, now)
		if err != nil || credential != nil {
			t.Fatalf("%s: credential applied to a non-GitHub-HTTPS origin: %v %v", origin, credential, err)
		}
	}
	jr.providerCredential = nil
	if credential, err := jr.pushCredential("https://github.com/octo/example.git", now); err != nil || credential != nil {
		t.Fatalf("missing credential changed the push path: %v %v", credential, err)
	}
}

func TestPushCredentialIsBoundToExactlyOneHost(t *testing.T) {
	now := time.Now()
	for name, tc := range map[string]struct {
		installations map[string]config.SourcePublicationInstallationConfig
		offered       []string
		refused       []string
	}{
		"github.com default": {
			installations: map[string]config.SourcePublicationInstallationConfig{
				"installation-1": {Provider: "github", APIBaseURL: "https://api.github.com"},
			},
			offered: []string{"https://github.com/octo/example.git"},
			refused: []string{"https://ghe.example.com/octo/example.git"},
		},
		"enterprise git_host": {
			installations: map[string]config.SourcePublicationInstallationConfig{
				"installation-1": {Provider: "github", APIBaseURL: "https://ghe.example.com/api/v3", GitHost: "ghe.example.com"},
			},
			offered: []string{"https://ghe.example.com/octo/example.git"},
			refused: []string{"https://github.com/octo/example.git"},
		},
		"enterprise without git_host": {
			installations: map[string]config.SourcePublicationInstallationConfig{
				"installation-1": {Provider: "github", APIBaseURL: "https://ghe.example.com/api/v3"},
			},
			offered: []string{"https://github.com/octo/example.git"},
			refused: []string{"https://ghe.example.com/octo/example.git"},
		},
		"ambiguous hosts": {
			installations: map[string]config.SourcePublicationInstallationConfig{
				"installation-1": {Provider: "github", APIBaseURL: "https://api.github.com"},
				"installation-2": {Provider: "github", APIBaseURL: "https://ghe.example.com/api/v3", GitHost: "ghe.example.com"},
			},
			refused: []string{"https://github.com/octo/example.git", "https://ghe.example.com/octo/example.git"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			jr.d.cfg.SourcePublicationInstallations = tc.installations
			jr.providerCredential = testProviderCredential(daemonProviderToken)
			for _, origin := range tc.offered {
				if credential, err := jr.pushCredential(origin, now); err != nil || credential == nil {
					t.Fatalf("%s: credential not offered: %v", origin, err)
				}
			}
			for _, origin := range tc.refused {
				if credential, err := jr.pushCredential(origin, now); err != nil || credential != nil {
					t.Fatalf("%s: credential offered to another host", origin)
				}
			}
		})
	}
}

func TestPushCredentialNearExpiryIsRetryableWithoutPushing(t *testing.T) {
	for name, expiresIn := range map[string]time.Duration{"near expiry": 30 * time.Second, "expired": -time.Second} {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			jr.providerCredential = testProviderCredential(daemonProviderToken)
			jr.providerCredential.ExpiresAt = time.Now().Add(expiresIn)
			rm := repo.NewManager(t.TempDir())
			workDir := t.TempDir()
			initRunCheckRepo(t, workDir)
			head := gitHead(t, workDir)
			err := jr.pushBranch(context.Background(), checkedOutRepository{
				ID: "repo-1", URL: "https://github.com/octo/example.git", Branch: "job/j1/a1",
				WorkDir: workDir, BaseSHA: head,
			}, rm, head)
			if !errors.Is(err, errProviderCredentialUnavailable) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), daemonProviderToken) {
				t.Fatalf("error echoes the token: %v", err)
			}
		})
	}
}

func TestProviderCredentialUnavailableFinishesAsBuildFinalizationFailure(t *testing.T) {
	jr, _ := testJobRunner(t)
	event := jr.jobFinishedEvent(buildFinalizationFailure(fmt.Errorf("push: %w", errProviderCredentialUnavailable)))
	if event.State != v1.JobFinishedFailed || event.FailureCode != v1.JobFailureBuildFinalization ||
		event.Retryable != nil || event.FailureCategory != "" ||
		event.FailureMessage != "provider credential unavailable at push time" {
		t.Fatalf("event = %+v", event)
	}
	if err := validation.JobFinishedEvent(event); err != nil {
		t.Fatalf("event rejected: %v", err)
	}
	other := jr.jobFinishedEvent(buildFinalizationFailure(errors.New("push rejected")))
	if other.FailureCode != v1.JobFailureBuildFinalization || other.FailureMessage != "build finalization failed" {
		t.Fatalf("event = %+v", other)
	}
}

func TestRedeliveredTerminalCommandReplaysAckDespiteExpiredCredential(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	logs := captureDaemonLogs(d)
	publisher := &fakeSourcePublicationPublisher{
		providers: []string{"github"},
		outcome:   v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationPermanent, Code: v1.SourceControlPermissionDenied},
	}
	d.sourcePublicationPublisher = publisher
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
	envelope := commandEnvelope("source-publication-redelivered", command)
	envelope.ProviderCredential = testProviderCredential(daemonProviderToken)
	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	d.sourcePublicationCommands.Wait()
	first := commandReceipt(t, d, envelope)
	if first.State != journal.CommandAccepted || publisher.calls != 1 {
		t.Fatalf("first receipt=%+v calls=%d", first, publisher.calls)
	}

	redelivered := envelope
	redelivered.SentAt = time.Now()
	expired := *testProviderCredential(daemonProviderToken)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	redelivered.ProviderCredential = &expired
	if err := d.OnCommand(context.Background(), redelivered); err != nil {
		t.Fatal(err)
	}
	d.sourcePublicationCommands.Wait()
	if replayed := commandReceipt(t, d, envelope); replayed != first {
		t.Fatalf("replayed receipt=%+v want=%+v", replayed, first)
	}
	if publisher.calls != 1 {
		t.Fatalf("redelivery re-ran the effect: calls=%d", publisher.calls)
	}
	if strings.Contains(logs.String(), "command envelope rejected") {
		t.Fatalf("terminal redelivery was rejected for its credential: %s", logs.String())
	}

	fresh := commandEnvelope("source-publication-expired-fresh", command)
	fresh.ProviderCredential = &expired
	if err := d.OnCommand(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	d.sourcePublicationCommands.Wait()
	if publisher.calls != 1 {
		t.Fatal("a new command with an expired credential reached the publisher")
	}
	if receipt := commandReceipt(t, d, fresh); receipt.State != journal.CommandRejected ||
		receipt.AckReasonCode != v1.CommandAckReasonInvalidCommand {
		t.Fatalf("fresh expired credential receipt = %+v", receipt)
	}
	if !strings.Contains(logs.String(), "command envelope rejected") {
		t.Fatalf("new command with an expired credential was not rejected: %s", logs.String())
	}
	if strings.Contains(logs.String(), daemonProviderToken) {
		t.Fatal("logs echo the token")
	}
}

func TestSourcePublicationPassesCommandCredentialToPublisher(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	publisher := &fakeSourcePublicationPublisher{
		providers: []string{"github"},
		outcome:   v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationPermanent, Code: v1.SourceControlPermissionDenied},
	}
	d.sourcePublicationPublisher = publisher
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
	credential := testProviderCredential(daemonProviderToken)
	if err := d.executeSourcePublication(context.Background(), "source-publication-credential", *command.SourcePublication, credential); err != nil {
		t.Fatal(err)
	}
	if publisher.credential != credential {
		t.Fatal("source publication did not receive the command credential")
	}
}

func TestContinuationCommandsCarryProviderCredential(t *testing.T) {
	continuation := validReviewFeedbackContinuationDaemonCommand()
	reviewJob := withTestAuthority(v1.JobSpec{
		JobID: "job-review-comment", AttemptID: "attempt-review-comment", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:       v1.TicketContext{Key: "PROJ-1", Summary: "Feature"}, ReviewComment: "Please fix",
	})
	for name, command := range map[string]v1.CommandPayload{
		"review_feedback_continuation": continuation,
		"review_comment":               {Command: v1.CmdReviewComment, Job: &reviewJob},
	} {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			d := jr.d
			d.leases = lease.NewManager()
			d.cfg.AllowUnsafeDirect = true
			credential := testProviderCredential(daemonProviderToken)
			envelope := commandEnvelope("continuation-with-credential", command)
			envelope.ProviderCredential = credential
			if err := d.executeCommand(context.Background(), envelope, command); err != nil {
				t.Fatal(err)
			}
			d.jobMu.Lock()
			active := d.activeJob
			d.jobMu.Unlock()
			if active == nil || active.providerCredential != credential {
				t.Fatal("continuation did not carry the provider credential to the job runner")
			}
			d.cancelActiveJob("test complete")
			deadline := time.Now().Add(10 * time.Second)
			for {
				d.jobMu.Lock()
				running := d.activeJob != nil
				d.jobMu.Unlock()
				if !running {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("job did not stop")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestInFlightRedeliveryWithExpiredCredentialIsNeverRejected(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	logs := captureDaemonLogs(d)
	publisher := &blockingSourcePublicationPublisher{
		started: make(chan struct{}),
		release: make(chan struct{}),
		outcome: v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationPermanent, Code: v1.SourceControlPermissionDenied},
	}
	d.sourcePublicationPublisher = publisher
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
	envelope := commandEnvelope("source-publication-in-flight", command)
	envelope.ProviderCredential = testProviderCredential(daemonProviderToken)
	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not start")
	}

	redelivered := envelope
	redelivered.SentAt = time.Now()
	expired := *testProviderCredential(daemonProviderToken)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	redelivered.ProviderCredential = &expired
	if err := d.OnCommand(context.Background(), redelivered); err != nil {
		t.Fatal(err)
	}
	if receipt := commandReceipt(t, d, envelope); receipt.State != journal.CommandProcessing {
		t.Fatalf("in-flight receipt = %+v", receipt)
	}
	if strings.Contains(logs.String(), "command envelope rejected") {
		t.Fatalf("in-flight redelivery was rejected: %s", logs.String())
	}
	close(publisher.release)
	d.sourcePublicationCommands.Wait()
	if receipt := commandReceipt(t, d, envelope); receipt.State != journal.CommandAccepted {
		t.Fatalf("final receipt = %+v", receipt)
	}
}

func TestRecoveredProcessingCommandWithExpiredCredentialRunsWithoutIt(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	logs := captureDaemonLogs(d)
	publisher := &fakeSourcePublicationPublisher{
		providers: []string{"github"},
		outcome:   v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationRetryable, Code: v1.SourceControlProviderUnavailable},
	}
	d.sourcePublicationPublisher = publisher
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
	envelope := commandEnvelope("source-publication-recovered", command)
	persistUnexecutedCommand(t, d, envelope, "process-old")
	expired := *testProviderCredential(daemonProviderToken)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	envelope.ProviderCredential = &expired
	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	d.sourcePublicationCommands.Wait()
	if receipt := commandReceipt(t, d, envelope); receipt.State != journal.CommandAccepted {
		t.Fatalf("recovered receipt = %+v", receipt)
	}
	if publisher.calls != 1 || publisher.credential != nil {
		t.Fatalf("calls=%d credential passed=%v", publisher.calls, publisher.credential != nil)
	}
	if strings.Contains(logs.String(), "command envelope rejected") || strings.Contains(logs.String(), daemonProviderToken) {
		t.Fatalf("logs = %s", logs.String())
	}
}
