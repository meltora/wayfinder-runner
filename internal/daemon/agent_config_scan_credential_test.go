package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/scanner"
)

type recordedScanRead struct {
	mu         sync.Mutex
	calls      int
	url        string
	credential *repo.GitHTTPSCredential
}

func (r *recordedScanRead) reader(result error) agentConfigScanReadFunc {
	return func(_ context.Context, url, _ string, credential *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		r.mu.Lock()
		r.calls++
		r.url = url
		if credential != nil {
			copied := *credential
			r.credential = &copied
		}
		r.mu.Unlock()
		if result != nil {
			return "", scanner.Inventory{}, result
		}
		return strings.Repeat("a", 40), scanner.Inventory{Items: []v1.AgentConfigDiscoveryItem{}}, nil
	}
}

func gitHubScanDaemon(t *testing.T, url string) *Daemon {
	t.Helper()
	d := scanDaemon(t)
	d.cfg.Repositories = map[string]config.RepositoryConfig{"repository-1": {URL: url}}
	return d
}

func runCredentialScan(t *testing.T, d *Daemon, credential *v1.ProviderCredential) (v1.Envelope, v1.AgentConfigScanOutcome) {
	t.Helper()
	env := scanEnvelope(validScanCommand())
	env.ProviderCredential = credential
	if err := d.OnCommand(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	return env, scanResult(t, d, env.ID).Outcome
}

func TestAgentConfigScanPassesRepositoryReadCredentialToGitHubOrigin(t *testing.T) {
	d := gitHubScanDaemon(t, "https://github.com/acme/api.git")
	recorded := &recordedScanRead{}
	d.agentConfigScanReader = recorded.reader(nil)
	credential := repositoryReadCredential(time.Now().Add(time.Hour))
	env, outcome := runCredentialScan(t, d, credential)
	if outcome.Kind != "discovered" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if recorded.calls != 1 || recorded.credential == nil || recorded.credential.Username != "x-access-token" ||
		recorded.credential.Password != repositoryReadToken ||
		!recorded.credential.NotAfter.Equal(credential.ExpiresAt.Add(-v1.ProviderCredentialMinimumRemainingLifetime)) {
		t.Fatal("scan reader did not receive the repository read credential")
	}
	record, found, err := d.journal.LoadRecord(context.Background(), v1.AgentConfigScanResultEventID(env.ID))
	if err != nil || !found || strings.Contains(string(record.Payload), repositoryReadToken) {
		t.Fatalf("scan result record holds the token or is missing: %v %v", found, err)
	}
	if matches := treeContainsToken(t, d.cfg.StateDir); len(matches) != 0 {
		t.Fatalf("token reached runner state: %v", matches)
	}
}

func TestAgentConfigScanIgnoresCredentialForNonGitHubOrigin(t *testing.T) {
	for _, url := range []string{"https://example.invalid/repository.git", "ssh://git@github.com/acme/api.git", "https://user@github.com/acme/api.git"} {
		t.Run(url, func(t *testing.T) {
			d := gitHubScanDaemon(t, url)
			recorded := &recordedScanRead{}
			d.agentConfigScanReader = recorded.reader(nil)
			_, outcome := runCredentialScan(t, d, repositoryReadCredential(time.Now().Add(time.Hour)))
			if outcome.Kind != "discovered" || recorded.calls != 1 || recorded.credential != nil {
				t.Fatalf("outcome = %+v credential passed = %v", outcome, recorded.credential != nil)
			}
		})
	}
}

func TestAgentConfigScanMapsRepositoryCredentialFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		url        string
		credential *v1.ProviderCredential
		result     error
		code       string
		reads      int
	}{
		{"expiring credential", "https://github.com/acme/api.git", repositoryReadCredential(time.Now().Add(30 * time.Second)), nil, "repository_credential_unavailable", 0},
		{"remote asks without credential", "https://github.com/acme/api.git", nil, errors.Join(repo.ErrScanUnavailable, repo.ErrRepositoryCredentialUnavailable), "repository_credential_unavailable", 1},
		{"credential rejected", "https://github.com/acme/api.git", repositoryReadCredential(time.Now().Add(time.Hour)), errors.Join(repo.ErrScanUnavailable, repo.ErrRepositoryAccessDenied), "repository_access_denied", 1},
		{"credential expired before fetch", "https://github.com/acme/api.git", repositoryReadCredential(time.Now().Add(time.Hour)), errors.Join(repo.ErrScanUnavailable, repo.ErrRepositoryCredentialUnavailable), "repository_credential_unavailable", 1},
		{"other git failure", "https://github.com/acme/api.git", repositoryReadCredential(time.Now().Add(time.Hour)), repo.ErrScanUnavailable, "checkout_failed", 1},
		{"limit wins", "https://github.com/acme/api.git", repositoryReadCredential(time.Now().Add(time.Hour)), errors.Join(repo.ErrScanLimit, repo.ErrRepositoryAccessDenied), "limit_exceeded", 1},
		{"non-github auth failure", "https://example.invalid/repository.git", nil, errors.Join(repo.ErrScanUnavailable, repo.ErrRepositoryCredentialUnavailable), "checkout_failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := gitHubScanDaemon(t, tc.url)
			recorded := &recordedScanRead{}
			d.agentConfigScanReader = recorded.reader(tc.result)
			env, outcome := runCredentialScan(t, d, tc.credential)
			if outcome.Kind != "failed" || outcome.Code != tc.code || outcome.Scan != nil || recorded.calls != tc.reads {
				t.Fatalf("outcome = %+v reads = %d", outcome, recorded.calls)
			}
			acknowledged, err := json.Marshal(scanResult(t, d, env.ID))
			if err != nil || strings.Contains(string(acknowledged), repositoryReadToken) {
				t.Fatalf("result event carries the token: %v", err)
			}
		})
	}
}

func TestAgentConfigScanStopWinsOverCredentialFailure(t *testing.T) {
	d := gitHubScanDaemon(t, "https://github.com/acme/api.git")
	d.agentConfigScanReader = func(ctx context.Context, _, _ string, _ *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		d.stopAgentConfigScan(errAgentConfigScanStopped)
		return "", scanner.Inventory{}, errors.Join(repo.ErrScanUnavailable, repo.ErrRepositoryAccessDenied)
	}
	_, outcome := runCredentialScan(t, d, repositoryReadCredential(time.Now().Add(time.Hour)))
	if outcome.Code != "stopped" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestAgentConfigScanWireEnvelopeWithCredentialIsAccepted(t *testing.T) {
	d := gitHubScanDaemon(t, "https://github.com/acme/api.git")
	recorded := &recordedScanRead{}
	d.agentConfigScanReader = recorded.reader(nil)
	env := scanEnvelope(validScanCommand())
	encoded, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &frame); err != nil {
		t.Fatal(err)
	}
	frame["provider_credential"] = json.RawMessage(`{"kind":"github_installation_token","token":"` + repositoryReadToken + `","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","repository_ids":["111"]}`)
	wire, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	var decoded v1.Envelope
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := d.OnCommand(context.Background(), decoded); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	receipt, claimed, err := d.journal.ClaimCommand(context.Background(), env.ID, "probe")
	if err != nil || claimed || receipt.AckStatus != "accepted" {
		t.Fatalf("receipt = %+v %v %v", receipt, claimed, err)
	}
	if outcome := scanResult(t, d, env.ID).Outcome; outcome.Kind != "discovered" || recorded.credential == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	if matches := treeContainsToken(t, d.cfg.StateDir); len(matches) != 0 {
		t.Fatalf("token reached runner state: %v", matches)
	}
}
