package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/repo"
)

const repositoryReadToken = "ghs_repositoryread0000000000000000000000"

func repositoryReadCredential(expiresAt time.Time) *v1.ProviderCredential {
	return &v1.ProviderCredential{
		Kind:          v1.ProviderCredentialGitHubInstallationToken,
		Token:         repositoryReadToken,
		ExpiresAt:     expiresAt,
		RepositoryIDs: []string{"111", "222"},
	}
}

func TestRepositoryCredentialAppliesOnlyToGitHubHTTPSOrigins(t *testing.T) {
	now := time.Now()
	usable := repositoryReadCredential(now.Add(time.Hour))
	expiring := repositoryReadCredential(now.Add(30 * time.Second))
	enterprise := map[string]config.SourcePublicationInstallationConfig{"one": {Provider: "github", GitHost: "git.example.com"}}
	for _, tc := range []struct {
		name          string
		credential    *v1.ProviderCredential
		url           string
		installations map[string]config.SourcePublicationInstallationConfig
		want          bool
		gitHub        bool
		err           error
	}{
		{"usable github", usable, "https://github.com/acme/api.git", nil, true, true, nil},
		{"no credential", nil, "https://github.com/acme/api.git", nil, false, true, nil},
		{"expiring", expiring, "https://github.com/acme/api.git", nil, false, true, repo.ErrRepositoryCredentialUnavailable},
		{"ssh", usable, "ssh://git@github.com/acme/api.git", nil, false, false, nil},
		{"userinfo", usable, "https://user@github.com/acme/api.git", nil, false, false, nil},
		{"other host", usable, "https://gitlab.com/acme/api.git", nil, false, false, nil},
		{"local", usable, "/srv/git/api.git", nil, false, false, nil},
		{"enterprise host", usable, "https://git.example.com/acme/api.git", enterprise, true, true, nil},
		{"github under enterprise config", usable, "https://github.com/acme/api.git", enterprise, false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, gitHub, err := repositoryCredential(tc.credential, tc.url, tc.installations, now)
			if !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) || gitHub != tc.gitHub || (got != nil) != tc.want {
				t.Fatalf("repositoryCredential = %v %v %v", got, gitHub, err)
			}
			if got != nil {
				if got.Username != "x-access-token" || got.Password != repositoryReadToken ||
					!got.NotAfter.Equal(tc.credential.ExpiresAt.Add(-v1.ProviderCredentialMinimumRemainingLifetime)) {
					t.Fatal("credential does not carry the token and its usable lifetime")
				}
			}
		})
	}
}

type recordedRemoteGit struct {
	url        string
	credential repo.GitHTTPSCredential
	args       []string
}

type fakeGitHubRemotes struct {
	t       *testing.T
	origins map[string]string
	failure error
	mu      sync.Mutex
	calls   []recordedRemoteGit
}

func (f *fakeGitHubRemotes) run(_ context.Context, _ string, remoteURL string, credential repo.GitHTTPSCredential, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, recordedRemoteGit{url: remoteURL, credential: credential, args: slices.Clone(args)})
	f.mu.Unlock()
	if f.failure != nil {
		return "", f.failure
	}
	if args[0] == "clone" {
		mirror := args[len(args)-1]
		buildWorkspaceGit(f.t, "", "clone", "--quiet", "--mirror", f.origins[remoteURL], mirror)
		buildWorkspaceGit(f.t, mirror, "remote", "set-url", "origin", remoteURL)
	}
	return "", nil
}

func gitHubCredentialJobRunner(t *testing.T, kind v1.JobKind, credential *v1.ProviderCredential, failure error) (*jobRunner, *capturedEvents, *fakeGitHubRemotes, map[string]string) {
	t.Helper()
	jr, events := testJobRunner(t)
	jr.d.supervisor = &compositionTestSupervisor{}
	jr.d.cfg.Provider = "claude"
	jr.d.cfg.Isolation = "direct"
	jr.job.Kind = kind
	jr.job.Ticket = v1.TicketContext{Key: "PROJ-1", Summary: "Read private repositories"}
	urls := map[string]string{"repo-1": "https://github.com/acme/api.git", "repo-2": "https://github.com/acme/web.git"}
	remotes := &fakeGitHubRemotes{t: t, origins: map[string]string{}, failure: failure}
	shas := map[string]string{}
	jr.d.cfg.Repositories = map[string]config.RepositoryConfig{}
	jr.job.Repositories = nil
	for _, id := range []string{"repo-1", "repo-2"} {
		origin := t.TempDir()
		buildWorkspaceGit(t, origin, "init", "-b", "main")
		buildWorkspaceGit(t, origin, "config", "user.email", "runner@example.test")
		buildWorkspaceGit(t, origin, "config", "user.name", "Runner Test")
		writeFixtureFile(t, origin, "README.md", id+"\n", 0o644)
		buildWorkspaceGit(t, origin, "add", "-A")
		buildWorkspaceGit(t, origin, "commit", "-m", id)
		shas[id] = buildWorkspaceGit(t, origin, "rev-parse", "HEAD")
		remotes.origins[urls[id]] = origin
		jr.d.cfg.Repositories[id] = config.RepositoryConfig{URL: urls[id]}
		jr.job.Repositories = append(jr.job.Repositories, v1.JobRepository{RepositoryID: id, BaseBranch: "main", BaseSHA: shas[id], Primary: id == "repo-1"})
	}
	jr.providerCredential = credential
	jr.d.configureRepositoryManager = func(manager *repo.Manager) { manager.RemoteGit = remotes.run }
	return jr, events, remotes, shas
}

func TestPrepareWorkspacePassesJobCredentialToEachGitHubRepository(t *testing.T) {
	credential := repositoryReadCredential(time.Now().Add(time.Hour))
	for _, kind := range []v1.JobKind{v1.JobKindPlan, v1.JobKindBuild} {
		t.Run(string(kind), func(t *testing.T) {
			jr, _, remotes, shas := gitHubCredentialJobRunner(t, kind, credential, nil)
			workDir, cleanup, err := jr.prepareWorkspace(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			if got := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD"); got != shas["repo-1"] {
				t.Fatalf("primary checkout = %s", got)
			}
			if got := buildWorkspaceGit(t, filepath.Join(jr.workDir, "repositories", "repo-2"), "rev-parse", "HEAD"); got != shas["repo-2"] {
				t.Fatalf("second checkout = %s", got)
			}
			if len(remotes.calls) != 2 {
				t.Fatalf("remote calls = %d", len(remotes.calls))
			}
			for index, url := range []string{"https://github.com/acme/api.git", "https://github.com/acme/web.git"} {
				call := remotes.calls[index]
				if call.url != url || call.args[0] != "clone" || call.credential.Username != "x-access-token" ||
					call.credential.Password != repositoryReadToken ||
					!call.credential.NotAfter.Equal(credential.ExpiresAt.Add(-v1.ProviderCredentialMinimumRemainingLifetime)) {
					t.Fatalf("remote call %d = %s %q", index, call.url, call.args)
				}
			}
			if matches := treeContainsToken(t, jr.d.cfg.StateDir); len(matches) != 0 {
				t.Fatalf("token reached runner state: %v", matches)
			}
			if matches := treeContainsToken(t, jr.workDir); len(matches) != 0 {
				t.Fatalf("token reached the job workspace: %v", matches)
			}
		})
	}
}

func treeContainsToken(t *testing.T, root string) []string {
	t.Helper()
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		// #nosec G304 -- test reads files it created under t.TempDir.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), repositoryReadToken) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return matches
}

func TestPrepareWorkspaceRepositoryCredentialFailuresEndJobWithFixedMessages(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential *v1.ProviderCredential
		failure    error
		message    string
		remote     bool
	}{
		{"credential expiring", repositoryReadCredential(time.Now().Add(30 * time.Second)), nil, "repository credential unavailable", false},
		{"no credential and remote asks", nil, errors.New("git clone: exit status 128: fatal: could not read Username for 'https://github.com': terminal prompts disabled"), "repository credential unavailable", false},
		{"rejected credential", repositoryReadCredential(time.Now().Add(time.Hour)), fmt.Errorf("%w: git clone: exit status 128", repo.ErrRepositoryAccessDenied), "repository access denied", true},
		{"unavailable sentinel", repositoryReadCredential(time.Now().Add(time.Hour)), repo.ErrRepositoryCredentialUnavailable, "repository credential unavailable", true},
		{"other failure", repositoryReadCredential(time.Now().Add(time.Hour)), errors.New("git clone: exit status 128: fatal: early EOF"), "workspace preparation failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jr, events, remotes, _ := gitHubCredentialJobRunner(t, v1.JobKindPlan, tc.credential, tc.failure)
			if tc.credential == nil {
				jr.d.configureRepositoryManager = func(manager *repo.Manager) {
					manager.Git = func(context.Context, string, ...string) (string, error) { return "", tc.failure }
				}
			}
			jr.run()
			finished := events.byKind("job_finished")
			if len(finished) != 1 {
				t.Fatalf("job_finished events = %d", len(finished))
			}
			event := finished[0]
			if event["state"] != "failed" || event["failure_code"] != "workspace_preparation_failed" || event["failure_message"] != tc.message {
				t.Fatalf("job_finished = %v", event)
			}
			if (len(remotes.calls) > 0) != tc.remote {
				t.Fatalf("remote calls = %d", len(remotes.calls))
			}
			encoded, err := json.Marshal(events.events)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), repositoryReadToken) {
				t.Fatal("events carry the token")
			}
			if _, err := os.Stat(jr.workDir); !os.IsNotExist(err) {
				t.Fatalf("job root survived: %v", err)
			}
		})
	}
}

func TestPrepareWorkspaceKeepsGenericMessageForNonGitHubAuthenticationFailure(t *testing.T) {
	jr, events, _, _ := gitHubCredentialJobRunner(t, v1.JobKindPlan, nil, nil)
	jr.d.cfg.Repositories["repo-1"] = config.RepositoryConfig{URL: "https://gitlab.com/acme/api.git"}
	jr.d.configureRepositoryManager = func(manager *repo.Manager) {
		manager.Git = func(context.Context, string, ...string) (string, error) {
			return "", errors.New("fatal: could not read Username for 'https://gitlab.com': terminal prompts disabled")
		}
	}
	jr.run()
	finished := events.byKind("job_finished")
	if len(finished) != 1 || finished[0]["failure_message"] != "workspace preparation failed" {
		t.Fatalf("job_finished = %v", finished)
	}
}

func TestPrepareWorkspaceFailureLogNeverCarriesToken(t *testing.T) {
	jr, events, _, _ := gitHubCredentialJobRunner(t, v1.JobKindPlan, repositoryReadCredential(time.Now().Add(time.Hour)),
		fmt.Errorf("%w: git clone: fatal: unable to access 'https://x-access-token:%s@github.com/acme/api.git/'", repo.ErrRepositoryAccessDenied, repositoryReadToken))
	var logs strings.Builder
	jr.logger = slog.New(slog.NewTextHandler(&logs, nil))
	jr.run()
	if strings.Contains(logs.String(), repositoryReadToken) || !strings.Contains(logs.String(), "workspace preparation failed") {
		t.Fatalf("runner log carries the token or misses the failure:\n%s", logs.String())
	}
	finished := events.byKind("job_finished")
	if len(finished) != 1 || finished[0]["failure_message"] != "repository access denied" {
		t.Fatalf("job_finished = %v", finished)
	}
}
