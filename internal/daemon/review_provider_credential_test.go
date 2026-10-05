package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/sourcepublication"
)

type redirectToTestServer struct {
	target *httptest.Server
}

func (redirect redirectToTestServer) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = "https"
	clone.URL.Host = strings.TrimPrefix(redirect.target.URL, "https://")
	clone.Host = clone.URL.Host
	return redirect.target.Client().Transport.RoundTrip(clone)
}

func TestReviewCommandsCarryProviderCredential(t *testing.T) {
	credential := testProviderCredential(daemonProviderToken)
	cases := []struct {
		name    string
		id      string
		command func() v1.CommandPayload
		install func(*Daemon) func() *v1.ProviderCredential
	}{
		{
			name:    "reviewer identity verification",
			id:      "reviewer-identity-verification-command-" + strings.Repeat("a", 64),
			command: validReviewerIdentityVerificationCommand,
			install: func(d *Daemon) func() *v1.ProviderCredential {
				fake := &fakeReviewerIdentityVerifier{outcome: v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationRetryable, Code: v1.ReviewerIdentityProviderUnavailable}}
				d.reviewerIdentityVerifier = fake
				return func() *v1.ProviderCredential { return fake.credential }
			},
		},
		{
			name:    "provider reviewer assignment",
			id:      "provider-reviewer-assignment-command-" + strings.Repeat("a", 64),
			command: validProviderReviewerAssignmentCommand,
			install: func(d *Daemon) func() *v1.ProviderCredential {
				fake := &fakeProviderReviewerAssigner{outcome: v1.ProviderReviewerAssignmentResultOutcome{Kind: v1.ProviderReviewerAssignmentRetryable, Code: v1.ProviderReviewerAssignmentProviderUnavailable}}
				d.providerReviewerAssigner = fake
				return func() *v1.ProviderCredential { return fake.credential }
			},
		},
		{
			name:    "provider reviewer assignment reconciliation",
			id:      "provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("a", 64),
			command: validProviderReviewerAssignmentReconciliationCommand,
			install: func(d *Daemon) func() *v1.ProviderCredential {
				fake := &fakeProviderReviewerAssignmentReconciler{outcome: v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationRetryable, Code: v1.ProviderReviewerAssignmentReconciliationProviderUnavailable}}
				d.providerReviewerAssignmentReconciler = fake
				return fake.credential.Load
			},
		},
		{
			name:    "provider review observation",
			id:      "provider-review-observation-command-" + strings.Repeat("a", 64),
			command: validProviderReviewObservationCommand,
			install: func(d *Daemon) func() *v1.ProviderCredential {
				fake := &fakeProviderReviewObserver{outcome: v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationRetryable, Code: v1.ProviderReviewObservationProviderUnavailable}}
				d.providerReviewObserver = fake
				return fake.credential.Load
			},
		},
		{
			name:    "provider review feedback content",
			id:      "provider-review-feedback-content-command-" + strings.Repeat("a", 64),
			command: validProviderReviewFeedbackContentCommand,
			install: func(d *Daemon) func() *v1.ProviderCredential {
				fake := &fakeProviderReviewFeedbackContentReader{outcome: v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentRetryable, Code: v1.ProviderReviewFeedbackContentProviderUnavailable}}
				d.providerReviewFeedbackContentReader = fake
				return fake.credential.Load
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for _, sent := range []*v1.ProviderCredential{credential, nil} {
				jr, _ := testJobRunner(t)
				received := test.install(jr.d)
				jr.d.eventSink = func(context.Context, any) error { return nil }
				envelope := v1.Envelope{ID: test.id, ProviderCredential: sent}
				if err := jr.d.executeCommand(context.Background(), envelope, test.command()); err != nil {
					t.Fatalf("execute: %v", err)
				}
				if got := received(); got != sent {
					t.Fatalf("credential passed = %v, want %v", got, sent)
				}
			}
		})
	}
}

func TestReviewOperationNeverLogsOrEmitsTokenEchoedByProvider(t *testing.T) {
	var echoedTokens atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "Bearer "+daemonProviderToken {
			echoedTokens.Add(1)
		}
		echoed := request.Header.Get("Authorization")
		writer.Header().Set("X-Echo", echoed)
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(writer, `{"message":"upstream rejected `+echoed+`"}`)
	}))
	defer server.Close()
	registry, err := sourcepublication.NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github"},
	}, &http.Client{Transport: redirectToTestServer{target: server}})
	if err != nil {
		t.Fatal(err)
	}
	jr, _ := testJobRunner(t)
	logs := captureDaemonLogs(jr.d)
	jr.d.reviewerIdentityVerifier = registry
	var mu sync.Mutex
	var events []string
	jr.d.eventSink = func(_ context.Context, payload any) error {
		raw, _ := json.Marshal(payload)
		mu.Lock()
		events = append(events, string(raw))
		mu.Unlock()
		return nil
	}
	credential := testProviderCredential(daemonProviderToken)
	credential.RepositoryIDs = []string{"101"}

	rejectedID := "reviewer-identity-verification-command-" + strings.Repeat("7", 64)
	expired := testProviderCredential(daemonProviderToken)
	expired.ExpiresAt = time.Now().Add(-time.Second)
	if err := jr.d.OnCommand(context.Background(), v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: rejectedID, SentAt: time.Now(),
		Payload: validReviewerIdentityVerificationCommand(), ProviderCredential: expired,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), rejectedID) {
		t.Fatalf("log capture control missing the rejected command: %s", logs.String())
	}

	id := "reviewer-identity-verification-command-" + strings.Repeat("8", 64)
	if err := jr.d.OnCommand(context.Background(), v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: id, SentAt: time.Now(),
		Payload: validReviewerIdentityVerificationCommand(), ProviderCredential: credential,
	}); err != nil {
		t.Fatal(err)
	}
	jr.d.reviewerIdentityVerificationCommands.Wait()
	mu.Lock()
	emitted := strings.Join(events, "\n")
	mu.Unlock()
	if echoedTokens.Load() == 0 {
		t.Fatal("the provider never received and echoed the credential token")
	}
	if !strings.Contains(emitted, `"code":"provider_unavailable"`) {
		t.Fatalf("events = %s", emitted)
	}
	for name, text := range map[string]string{"logs": logs.String(), "events": emitted} {
		if strings.Contains(text, daemonProviderToken) {
			t.Fatalf("%s leaked the provider token: %s", name, text)
		}
	}
}
