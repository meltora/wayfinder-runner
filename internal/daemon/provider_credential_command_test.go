package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"
)

const daemonProviderToken = "ghs_daemon0000000000000000000000000000000"

func testProviderCredential(token string) *v1.ProviderCredential {
	return &v1.ProviderCredential{
		Kind:          v1.ProviderCredentialGitHubInstallationToken,
		Token:         token,
		ExpiresAt:     time.Now().Add(time.Hour),
		RepositoryIDs: []string{"123456789"},
	}
}

func captureDaemonLogs(d *Daemon) *bytes.Buffer {
	var logs bytes.Buffer
	d.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &logs
}

func stateFilesContain(t *testing.T, root string, needle string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		// #nosec G304 -- test reads files it created under t.TempDir.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(needle)) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestOnCommandRejectsInvalidProviderCredentialWithoutEchoingIt(t *testing.T) {
	cases := map[string]func(*v1.ProviderCredential){
		"malformed token": func(credential *v1.ProviderCredential) { credential.Token += " injected" },
		"expired":         func(credential *v1.ProviderCredential) { credential.ExpiresAt = time.Now().Add(-time.Second) },
		"unknown kind":    func(credential *v1.ProviderCredential) { credential.Kind = "github_app_jwt" },
		"named repository": func(credential *v1.ProviderCredential) {
			credential.RepositoryIDs = []string{"octo/example"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			jr, captured := testJobRunner(t)
			d := jr.d
			logs := captureDaemonLogs(d)
			publisher := &fakeSourcePublicationPublisher{providers: []string{"github"}}
			d.sourcePublicationPublisher = publisher
			command := validSourcePublicationCommand()
			command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
			envelope := commandEnvelope("source-publication-command-invalid-credential", command)
			envelope.ProviderCredential = testProviderCredential(daemonProviderToken)
			mutate(envelope.ProviderCredential)

			if err := d.OnCommand(context.Background(), envelope); err != nil {
				t.Fatal(err)
			}
			d.sourcePublicationCommands.Wait()

			if publisher.calls != 0 {
				t.Fatal("invalid provider credential reached the publisher")
			}
			if !strings.Contains(logs.String(), "command envelope rejected") ||
				!strings.Contains(logs.String(), v1.ErrInvalidProviderCredential.Error()) {
				t.Fatalf("logs = %s", logs.String())
			}
			if strings.Contains(logs.String(), daemonProviderToken) {
				t.Fatalf("logs echo the token: %s", logs.String())
			}
			if len(captured.events) != 0 {
				t.Fatal("rejected command emitted events")
			}
			if stateFilesContain(t, d.cfg.StateDir, daemonProviderToken) {
				t.Fatal("rejected provider credential reached durable state")
			}
		})
	}
}

func TestOnCommandKeepsProviderCredentialOutOfJournalLogsAndEvents(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	logs := captureDaemonLogs(d)
	publisher := &fakeSourcePublicationPublisher{
		providers: []string{"github"},
		outcome: v1.SourcePublicationResultOutcome{
			Kind: v1.SourcePublicationPermanent, Code: v1.SourceControlPermissionDenied,
		},
	}
	d.sourcePublicationPublisher = publisher
	var events []any
	d.eventSink = func(_ context.Context, payload any) error {
		events = append(events, payload)
		return nil
	}
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
	envelope := commandEnvelope("source-publication-command-with-credential", command)
	envelope.ProviderCredential = testProviderCredential(daemonProviderToken)

	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	d.sourcePublicationCommands.Wait()

	if publisher.calls != 1 || len(events) != 1 {
		t.Fatalf("publisher calls = %d, events = %d", publisher.calls, len(events))
	}
	if receipt := commandReceipt(t, d, envelope); receipt.AckStatus != string(v1.CommandAckAccepted) {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !stateFilesContain(t, d.cfg.StateDir, envelope.ID) {
		t.Fatal("durable command record was not found on disk")
	}
	if stateFilesContain(t, d.cfg.StateDir, daemonProviderToken) {
		t.Fatal("provider credential reached durable state")
	}
	encodedEvents, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{logs.String(), string(encodedEvents), fmt.Sprintf("%+v", events)} {
		if strings.Contains(output, daemonProviderToken) {
			t.Fatalf("provider credential escaped: %s", output)
		}
	}
}

func TestDurableCommandRecordPayloadOmitsProviderCredential(t *testing.T) {
	envelope := commandEnvelope("source-publication-command-1", validSourcePublicationCommand())
	envelope.ProviderCredential = testProviderCredential(daemonProviderToken)
	digest, err := commandContentDigest(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	record, err := durableCommandRecordPayload(envelope, digest)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(record, []byte(daemonProviderToken)) || bytes.Contains(record, []byte("provider_credential")) {
		t.Fatalf("durable command record = %s", record)
	}
	if envelope.ProviderCredential == nil {
		t.Fatal("stripping the durable record mutated the in-memory envelope")
	}
	withoutCredential := envelope
	withoutCredential.ProviderCredential = nil
	baselineDigest, err := commandContentDigest(withoutCredential.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if baselineDigest != digest {
		t.Fatal("provider credential changed the command content digest")
	}
}

func TestStartJobCarriesProviderCredentialInMemoryOnly(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	logs := captureDaemonLogs(d)
	d.leases = lease.NewManager()
	d.cfg.AllowUnsafeDirect = true
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-credential", AttemptID: "attempt-credential", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:       v1.TicketContext{Key: "PROJ-1", Summary: "Feature"},
	})
	credential := testProviderCredential(daemonProviderToken)
	envelope := commandEnvelope("start-with-credential", v1.CommandPayload{Command: v1.CmdStartJob, Job: &job})
	envelope.ProviderCredential = credential

	if err := d.executeCommand(context.Background(), envelope, v1.CommandPayload{Command: v1.CmdStartJob, Job: &job}); err != nil {
		t.Fatal(err)
	}
	d.jobMu.Lock()
	active := d.activeJob
	d.jobMu.Unlock()
	if active == nil || active.providerCredential != credential {
		t.Fatal("start_job did not carry the provider credential to the job runner")
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
	if strings.Contains(logs.String(), daemonProviderToken) {
		t.Fatalf("logs echo the token: %s", logs.String())
	}
	if stateFilesContain(t, d.cfg.StateDir, daemonProviderToken) {
		t.Fatal("provider credential reached job state on disk")
	}
}
