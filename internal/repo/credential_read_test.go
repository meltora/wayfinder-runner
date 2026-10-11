package repo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const wrongReadToken = "ghs_wrong00000000000000000000000000000000"

type gitInvocation struct {
	args []string
	env  []string
}

type gitRecorder struct {
	mu    sync.Mutex
	calls []gitInvocation
}

func (r *gitRecorder) observe(args, env []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, gitInvocation{args: slices.Clone(args), env: slices.Clone(env)})
}

func (r *gitRecorder) snapshot() []gitInvocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (call gitInvocation) subcommand() string {
	for index := 0; index < len(call.args); index++ {
		arg := call.args[index]
		if arg == "-c" {
			index++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

func (call gitInvocation) carriesCredential() bool {
	for _, entry := range call.env {
		if strings.HasPrefix(entry, "SHORTSTACK_") || strings.HasPrefix(entry, "GIT_ASKPASS=") {
			return true
		}
	}
	for _, arg := range call.args {
		if strings.HasPrefix(arg, "core.askPass=") {
			return true
		}
	}
	return false
}

func readCredential() *GitHTTPSCredential {
	return &GitHTTPSCredential{Username: "x-access-token", Password: pushToken}
}

func newPrivateScanOrigin(t *testing.T, fixture *httpsGitFixture) (string, string) {
	t.Helper()
	source, sha := newScanFixture(t)
	bare := filepath.Join(fixture.projectDir, "origin.git")
	git(t, "", "clone", "--bare", source, bare)
	return fixture.server.URL + "/origin.git", sha
}

func newPrivateMirrorOrigin(t *testing.T, fixture *httpsGitFixture) (string, string) {
	t.Helper()
	source := seedOrigin(t)
	bare := filepath.Join(fixture.projectDir, "origin.git")
	git(t, "", "clone", "--bare", source, bare)
	return fixture.server.URL + "/origin.git", bare
}

func requireHTTPSFixture(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping HTTPS git fixture")
	}
}

func assertNoTokenOutsideAuthorization(t *testing.T, fixtures ...*httpsGitFixture) {
	t.Helper()
	for _, fixture := range fixtures {
		fixture.mu.Lock()
		leaked := fixture.tokenOutsideAuth
		fixture.mu.Unlock()
		if leaked {
			t.Fatal("token reached a fixture outside the Authorization header")
		}
	}
}

func assertTokenAbsent(t *testing.T, err error, roots ...string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), pushToken) {
		t.Fatalf("error echoes the token: %v", err)
	}
	for _, root := range roots {
		if matches := filesContaining(t, root, pushToken); len(matches) != 0 {
			t.Fatalf("token reached disk: %v", matches)
		}
	}
}

func TestClassifyGitAuthFailure(t *testing.T) {
	for _, tc := range []struct {
		stderr     string
		credential bool
		want       error
	}{
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", false, ErrRepositoryCredentialUnavailable},
		{"fatal: could not read Password for 'https://x-access-token@github.com': terminal prompts disabled", false, ErrRepositoryCredentialUnavailable},
		{"remote: Invalid username or token.\nfatal: Authentication failed for 'https://github.com/acme/api.git/'", false, ErrRepositoryCredentialUnavailable},
		{"error: RPC failed; HTTP 401\nfatal: unable to access 'https://github.com/acme/api.git/': The requested URL returned error: 401", false, ErrRepositoryCredentialUnavailable},
		{"fatal: unable to access 'https://github.com/acme/api.git/': The requested URL returned error: 403", false, nil},
		{"remote: Repository not found.\nfatal: repository 'https://github.com/acme/api.git/' not found", false, nil},
		{"fatal: unable to access 'https://github.com/acme/api.git/': Could not resolve host: github.com", false, nil},
		{"fatal: Remote branch missing not found in upstream origin", false, nil},
		{"", false, nil},
		{"fatal: Authentication failed for 'https://github.com/acme/api.git/'", true, ErrRepositoryAccessDenied},
		{"fatal: unable to access 'https://github.com/acme/api.git/': The requested URL returned error: 401", true, ErrRepositoryAccessDenied},
		{"fatal: unable to access 'https://github.com/acme/api.git/': The requested URL returned error: 403", true, ErrRepositoryAccessDenied},
		{"fatal: unable to access 'https://github.com/acme/api.git/': The requested URL returned error: 404", true, ErrRepositoryAccessDenied},
		{"remote: Repository not found.\nfatal: repository 'https://github.com/acme/api.git/' not found", true, ErrRepositoryAccessDenied},
		{"fatal: repository 'https://github.com/acme/api.git/' not found", true, ErrRepositoryAccessDenied},
		{"error: unable to read askpass response from '/usr/local/bin/our-runner'\nfatal: could not read Username for 'https://evil.invalid': terminal prompts disabled", true, ErrRepositoryAccessDenied},
		{"fatal: could not read Password for 'https://x-access-token@github.com': terminal prompts disabled", true, ErrRepositoryAccessDenied},
		{"fatal: unable to access 'https://github.com/acme/api.git/': Could not resolve host: github.com", true, nil},
		{"fatal: Remote branch missing not found in upstream origin", true, nil},
		{"fatal: unable to access 'https://github.com/acme/api.git/': The requested URL returned error: 500", true, nil},
		{"", true, nil},
	} {
		got := classifyGitAuthFailure([]byte(tc.stderr), tc.credential)
		if !errors.Is(got, tc.want) || (tc.want == nil) != (got == nil) {
			t.Fatalf("classifyGitAuthFailure(%q, %v) = %v, want %v", tc.stderr, tc.credential, got, tc.want)
		}
	}
}

func TestGitHTTPSCredentialRendersRedacted(t *testing.T) {
	credential := GitHTTPSCredential{Username: "x-access-token", Password: pushToken, NotAfter: time.Now().Add(time.Hour)}
	for _, rendered := range []string{
		credential.String(),
		credential.GoString(),
		fmt.Sprintf("%v", credential),
		fmt.Sprintf("%+v", credential),
		fmt.Sprintf("%#v", credential),
		fmt.Sprintf("%s", credential),
		fmt.Sprintf("%v", &credential),
		fmt.Sprintf("%+v", []GitHTTPSCredential{credential}),
	} {
		if strings.Contains(rendered, pushToken) || !strings.Contains(rendered, "[redacted]") {
			t.Fatalf("credential rendered as %q", rendered)
		}
	}
}

func TestScanAgentConfigWithCredentialOverHTTPS(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, sha := newPrivateScanOrigin(t, fixture)
	root := t.TempDir()
	m := NewManager(root)
	trustFixtureCA(t, m, fixture.caFile)
	recorder := &gitRecorder{}
	m.testGitObserver = recorder.observe

	got, inventory, err := m.ScanAgentConfigWithCredential(t.Context(), remoteURL, "refs/heads/main", readCredential())
	if err != nil || got != sha || len(inventory.Items) != 1 {
		t.Fatalf("credentialed scan = %q %#v %v", got, inventory, err)
	}
	fixture.mu.Lock()
	authorized := fixture.authorized
	fixture.mu.Unlock()
	if authorized == 0 {
		t.Fatal("fixture never saw an authorized request")
	}
	assertNoTokenOutsideAuthorization(t, fixture)
	assertTokenAbsent(t, nil, root)
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	networkCalls := 0
	for _, call := range recorder.snapshot() {
		for _, arg := range call.args {
			if strings.Contains(arg, pushToken) {
				t.Fatalf("argv contains the token: %q", call.args)
			}
		}
		switch call.subcommand() {
		case "clone", "fetch":
			networkCalls++
			for _, required := range []string{"credential.helper=", "core.askPass=" + helper, "http.followRedirects=false", "core.hooksPath=/dev/null", "core.fsmonitor=false", "fetch.fsckObjects=true", "transfer.fsckObjects=true", "core.attributesFile=/dev/null", "--no-pager", "--no-replace-objects"} {
				if !slices.Contains(call.args, required) {
					t.Fatalf("%s argv %q is missing %q", call.subcommand(), call.args, required)
				}
			}
			host := strings.TrimPrefix(fixture.server.URL, "https://")
			for _, required := range []string{
				"GIT_ASKPASS=" + helper,
				"SHORTSTACK_GIT_ASKPASS=1",
				"SHORTSTACK_GIT_HOST=" + host,
				"SHORTSTACK_GIT_USERNAME=x-access-token",
				"SHORTSTACK_GIT_PASSWORD=" + pushToken,
				"GIT_ALLOW_PROTOCOL=https",
				"LC_ALL=C",
				"LANGUAGE=",
				"GIT_TERMINAL_PROMPT=0",
				"GIT_CONFIG_NOSYSTEM=1",
				"GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_NO_REPLACE_OBJECTS=1",
				"GIT_LFS_SKIP_SMUDGE=1",
				"GIT_NO_LAZY_FETCH=1",
			} {
				if !slices.Contains(call.env, required) {
					t.Fatalf("%s env is missing %q", call.subcommand(), required)
				}
			}
		default:
			if call.carriesCredential() {
				t.Fatalf("local scan step %q received the credential", call.args)
			}
		}
	}
	if networkCalls != 2 {
		t.Fatalf("network calls = %d, want clone and fetch", networkCalls)
	}
}

func TestScanAgentConfigClassifiesHTTPSAuthenticationFailures(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, _ := newPrivateScanOrigin(t, fixture)
	for name, tc := range map[string]struct {
		credential *GitHTTPSCredential
		want       error
	}{
		"no credential": {nil, ErrRepositoryCredentialUnavailable},
		"wrong token":   {&GitHTTPSCredential{Username: "x-access-token", Password: wrongReadToken}, ErrRepositoryAccessDenied},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			m := NewManager(root)
			trustFixtureCA(t, m, fixture.caFile)
			_, inventory, err := m.ScanAgentConfigWithCredential(t.Context(), remoteURL, "refs/heads/main", tc.credential)
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrScanUnavailable) || len(inventory.Items) != 0 {
				t.Fatalf("scan error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), wrongReadToken) || strings.Contains(err.Error(), "fatal") || strings.Contains(err.Error(), fixture.server.URL) {
				t.Fatalf("scan error discloses git output: %v", err)
			}
			if matches := filesContaining(t, root, wrongReadToken); len(matches) != 0 {
				t.Fatalf("token reached disk: %v", matches)
			}
		})
	}
}

func TestScanAgentConfigRefusesUnusableCredentialWithoutNetwork(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, _ := newPrivateScanOrigin(t, fixture)
	m := NewManager(t.TempDir())
	trustFixtureCA(t, m, fixture.caFile)
	for _, credential := range []*GitHTTPSCredential{
		{Username: "x-access-token", Password: pushToken, NotAfter: time.Now().Add(-time.Second)},
		{Username: "x-access-token", Password: ""},
		{Username: "", Password: pushToken},
	} {
		before, _ := fixture.snapshot()
		_, _, err := m.ScanAgentConfigWithCredential(t.Context(), remoteURL, "refs/heads/main", credential)
		if !errors.Is(err, ErrRepositoryCredentialUnavailable) {
			t.Fatalf("unusable credential error = %v", err)
		}
		if after, _ := fixture.snapshot(); after != before {
			t.Fatal("unusable credential reached the network")
		}
	}
}

func TestScanAgentConfigRefusesCredentialExpiringBeforeFetch(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, _ := newPrivateScanOrigin(t, fixture)
	m := NewManager(t.TempDir())
	trustFixtureCA(t, m, fixture.caFile)
	credential := readCredential()
	m.testGitObserver = func(args, _ []string) {
		if (gitInvocation{args: args}).subcommand() == "clone" {
			credential.NotAfter = time.Now().Add(-time.Second)
		}
	}
	_, _, err := m.ScanAgentConfigWithCredential(t.Context(), remoteURL, "refs/heads/main", credential)
	if !errors.Is(err, ErrRepositoryCredentialUnavailable) {
		t.Fatalf("expired credential before fetch = %v", err)
	}
}

func TestScanAgentConfigIgnoresCredentialForLocalOrigin(t *testing.T) {
	dir, sha := newScanFixture(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	recorder := &gitRecorder{}
	m.testGitObserver = recorder.observe
	got, _, err := m.ScanAgentConfigWithCredential(context.Background(), dir, "refs/heads/main", readCredential())
	if err != nil || got != sha {
		t.Fatalf("local scan with credential = %q %v", got, err)
	}
	calls := recorder.snapshot()
	if len(calls) == 0 {
		t.Fatal("recorder saw no git invocations")
	}
	for _, call := range calls {
		if call.carriesCredential() || slices.Contains(call.env, "LC_ALL=C") && !slices.Contains(os.Environ(), "LC_ALL=C") {
			t.Fatalf("local origin received credential wiring: %q %q", call.args, call.env)
		}
		for _, entry := range call.env {
			if strings.Contains(entry, pushToken) {
				t.Fatalf("local origin env holds the token: %q", entry)
			}
		}
	}
}

func TestScanAgentConfigWithoutCredentialKeepsExistingEnvironment(t *testing.T) {
	dir, _ := newScanFixture(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	recorder := &gitRecorder{}
	m.testGitObserver = recorder.observe
	if _, _, err := m.ScanAgentConfig(context.Background(), dir, "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	want := append(gitEnvironment(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_PROTOCOL_FROM_USER=0", "GIT_ALLOW_PROTOCOL=https:ssh:file", "GIT_NO_REPLACE_OBJECTS=1", "GIT_LFS_SKIP_SMUDGE=1", "GIT_NO_LAZY_FETCH=1")
	for _, call := range recorder.snapshot() {
		if !slices.Equal(call.env, want) {
			t.Fatalf("credential-free env changed for %q:\n%q\nwant\n%q", call.args, call.env, want)
		}
		if call.carriesCredential() || slices.Contains(call.args, "http.followRedirects=false") {
			t.Fatalf("credential-free argv changed: %q", call.args)
		}
	}
}

func TestScanAgentConfigPublicHTTPSWithoutCredential(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	fixture.mu.Lock()
	fixture.anonymousRead = true
	fixture.mu.Unlock()
	remoteURL, sha := newPrivateScanOrigin(t, fixture)
	m := NewManager(t.TempDir())
	trustFixtureCA(t, m, fixture.caFile)
	got, _, err := m.ScanAgentConfig(t.Context(), remoteURL, "refs/heads/main")
	if err != nil || got != sha {
		t.Fatalf("anonymous scan = %q %v", got, err)
	}
}

func TestScanAgentConfigInsteadOfNeverSendsTokenToRewrittenHost(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, _ := newPrivateScanOrigin(t, fixture)
	attacker := newHTTPSGitFixture(t)
	newPrivateScanOrigin(t, attacker)
	m := NewManager(t.TempDir())
	caFile := combinedCAFile(t, fixture, attacker)
	trustFixtureCA(t, m, caFile)
	m.testGitEnvironment = append(m.testGitEnvironment,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url."+attacker.server.URL+"/.insteadOf",
		"GIT_CONFIG_VALUE_0="+fixture.server.URL+"/",
	)
	_, _, err := m.ScanAgentConfigWithCredential(t.Context(), remoteURL, "refs/heads/main", readCredential())
	if err == nil {
		t.Fatal("scan through an insteadOf rewrite succeeded")
	}
	if requests, sawToken := attacker.snapshot(); requests == 0 || sawToken {
		t.Fatalf("rewritten host requests=%d token=%v", requests, sawToken)
	}
	if _, sawToken := fixture.snapshot(); sawToken {
		t.Fatal("expected host saw the token although git never contacted it")
	}
	assertTokenAbsent(t, err)
}

func TestScanAgentConfigDoesNotFollowRedirectsWithToken(t *testing.T) {
	requireHTTPSFixture(t)
	attacker := newHTTPSGitFixture(t)
	newPrivateScanOrigin(t, attacker)
	redirecting := newHTTPSGitFixtureWithHandler(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, attacker.server.URL+request.URL.RequestURI(), http.StatusFound)
	}))
	m := NewManager(t.TempDir())
	trustFixtureCA(t, m, combinedCAFile(t, redirecting, attacker))
	_, _, err := m.ScanAgentConfigWithCredential(t.Context(), redirecting.server.URL+"/origin.git", "refs/heads/main", readCredential())
	if err == nil {
		t.Fatal("scan through a redirect succeeded")
	}
	if requests, sawToken := attacker.snapshot(); requests != 0 || sawToken {
		t.Fatalf("redirect target received requests=%d token=%v", requests, sawToken)
	}
	if _, sawToken := redirecting.snapshot(); sawToken {
		t.Fatal("redirecting host received the token")
	}
	assertTokenAbsent(t, err)
}

func TestRegisterWithCredentialClonesAndFetchesOverHTTPS(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, bare := newPrivateMirrorOrigin(t, fixture)
	root := t.TempDir()
	m := NewManager(root)
	trustFixtureCA(t, m, fixture.caFile)
	recorder := &gitRecorder{}
	m.testGitObserver = recorder.observe
	var localCalls [][]string
	m.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		localCalls = append(localCalls, slices.Clone(args))
		return m.runGit(ctx, dir, args...)
	}

	mirror, err := m.RegisterWithCredential(t.Context(), remoteURL, readCredential())
	if err != nil {
		t.Fatalf("credentialed mirror clone: %v", err)
	}
	git(t, bare, "branch", "next", "main")
	if _, err := m.RegisterWithCredential(t.Context(), remoteURL, readCredential()); err != nil {
		t.Fatalf("credentialed mirror fetch: %v", err)
	}
	git(t, mirror, "rev-parse", "--verify", "refs/heads/next")

	calls := recorder.snapshot()
	if len(calls) != 2 || calls[0].subcommand() != "clone" || calls[1].subcommand() != "fetch" {
		t.Fatalf("credentialed calls = %+v", calls)
	}
	host := strings.TrimPrefix(fixture.server.URL, "https://")
	for _, call := range calls {
		for _, required := range []string{"credential.helper=", "http.followRedirects=false", "core.hooksPath=/dev/null"} {
			if !slices.Contains(call.args, required) {
				t.Fatalf("%s argv %q is missing %q", call.subcommand(), call.args, required)
			}
		}
		for _, required := range []string{"GIT_ALLOW_PROTOCOL=https", "LC_ALL=C", "LANGUAGE=", "SHORTSTACK_GIT_ASKPASS=1", "SHORTSTACK_GIT_HOST=" + host, "SHORTSTACK_GIT_USERNAME=x-access-token", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"} {
			if !slices.Contains(call.env, required) {
				t.Fatalf("%s env is missing %q", call.subcommand(), required)
			}
		}
		for _, arg := range call.args {
			if strings.Contains(arg, pushToken) {
				t.Fatalf("argv contains the token: %q", call.args)
			}
		}
	}
	want := [][]string{{"remote", "get-url", "origin"}, {"worktree", "prune"}}
	if !slices.EqualFunc(localCalls, want, slices.Equal[[]string]) {
		t.Fatalf("local calls = %q", localCalls)
	}
	assertNoTokenOutsideAuthorization(t, fixture)
	assertTokenAbsent(t, nil, root)
	config := gitOutputForTest(t, mirror, "config", "--list")
	origin := gitOutputForTest(t, mirror, "remote", "get-url", "origin")
	if strings.Contains(config, pushToken) || origin != remoteURL {
		t.Fatalf("mirror config holds the token or a rewritten origin: %s", origin)
	}

	dest := filepath.Join(t.TempDir(), "workspace")
	if err := m.CreateDetachedWorkspaceWithCredential(t.Context(), remoteURL, gitOutputForTest(t, bare, "rev-parse", "HEAD"), dest, readCredential()); err != nil {
		t.Fatalf("detached workspace: %v", err)
	}
	assertTokenAbsent(t, nil, root, dest)
	if config := gitOutputForTest(t, dest, "config", "--list"); strings.Contains(config, pushToken) {
		t.Fatal("workspace config holds the token")
	}
}

func TestCreateDetachedWorkspaceWithCredentialFetchesMissingCommit(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, bare := newPrivateMirrorOrigin(t, fixture)
	root := t.TempDir()
	m := NewManager(root)
	trustFixtureCA(t, m, fixture.caFile)
	if _, err := m.RegisterWithCredential(t.Context(), remoteURL, readCredential()); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	git(t, "", "clone", "--quiet", bare, source)
	git(t, source, "config", "user.email", "t@t")
	git(t, source, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(source, "observed.txt"), []byte("observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", ".")
	git(t, source, "commit", "-m", "observed after registration")
	git(t, source, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	expected := gitOutputForTest(t, source, "rev-parse", "HEAD")
	recorder := &gitRecorder{}
	m.testGitObserver = recorder.observe

	dest := filepath.Join(t.TempDir(), "workspace")
	if err := m.CreateDetachedWorkspaceWithCredential(t.Context(), remoteURL, expected, dest, readCredential()); err != nil {
		t.Fatalf("detached fetch: %v", err)
	}
	if got := gitOutputForTest(t, dest, "rev-parse", "HEAD"); got != expected {
		t.Fatalf("workspace HEAD = %s, want %s", got, expected)
	}
	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].subcommand() != "fetch" || !slices.Contains(calls[0].args, expected) {
		t.Fatalf("credentialed calls = %+v", calls)
	}
	assertNoTokenOutsideAuthorization(t, fixture)
	assertTokenAbsent(t, nil, root, dest)
}

func TestRegisterWithCredentialClassifiesAuthenticationFailures(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, _ := newPrivateMirrorOrigin(t, fixture)
	for name, tc := range map[string]struct {
		credential *GitHTTPSCredential
		want       error
	}{
		"no credential": {nil, ErrRepositoryCredentialUnavailable},
		"wrong token":   {&GitHTTPSCredential{Username: "x-access-token", Password: wrongReadToken}, ErrRepositoryAccessDenied},
		"expired":       {&GitHTTPSCredential{Username: "x-access-token", Password: pushToken, NotAfter: time.Now().Add(-time.Second)}, ErrRepositoryCredentialUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			m := NewManager(root)
			trustFixtureCA(t, m, fixture.caFile)
			_, err := m.RegisterWithCredential(t.Context(), remoteURL, tc.credential)
			if !errors.Is(err, tc.want) {
				t.Fatalf("register error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), wrongReadToken) || strings.Contains(err.Error(), pushToken) {
				t.Fatalf("register error echoes a token: %v", err)
			}
			assertTokenAbsent(t, err, root)
			if matches := filesContaining(t, root, wrongReadToken); len(matches) != 0 {
				t.Fatalf("token reached disk: %v", matches)
			}
		})
	}
}

func TestRegisterWithCredentialScrubsGitErrors(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL := fixture.server.URL + "/" + pushToken + ".git"
	root := t.TempDir()
	m := NewManager(root)
	trustFixtureCA(t, m, fixture.caFile)
	_, err := m.RegisterWithCredential(t.Context(), remoteURL, readCredential())
	if err == nil {
		t.Fatal("clone of a missing repository succeeded")
	}
	if strings.Contains(err.Error(), pushToken) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("git error was not scrubbed: %v", err)
	}
}

func TestRegisterWithCredentialIgnoresCredentialForNonHTTPSOrigin(t *testing.T) {
	origin := seedOrigin(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	recorder := &gitRecorder{}
	m.testGitObserver = recorder.observe
	if _, err := m.RegisterWithCredential(context.Background(), origin, readCredential()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RegisterWithCredential(context.Background(), origin, readCredential()); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateDetachedWorkspaceWithCredential(context.Background(), origin, gitOutputForTest(t, origin, "rev-parse", "HEAD"), filepath.Join(t.TempDir(), "workspace"), readCredential()); err != nil {
		t.Fatal(err)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("local origin used the credentialed runner: %+v", calls)
	}
	for _, remoteURL := range []string{"ssh://git@github.com/acme/api.git", "https://user@github.com/acme/api.git"} {
		if got, ok := applicableHTTPSCredential(remoteURL, readCredential()); ok || got != nil {
			t.Fatalf("credential applies to %q", remoteURL)
		}
	}
	if got, ok := applicableHTTPSCredential("https://github.com/acme/api.git", readCredential()); !ok || got == nil {
		t.Fatal("credential does not apply to a GitHub HTTPS origin")
	}
}

func TestCredentialFetchRefusesMirrorConfigOutsideAllowList(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, _ := newPrivateMirrorOrigin(t, fixture)
	attacker := newHTTPSGitFixture(t)
	newPrivateMirrorOrigin(t, attacker)
	caFile := combinedCAFile(t, fixture, attacker)
	for _, entry := range []struct{ key, value string }{
		{"url." + attacker.server.URL + "/.insteadOf", fixture.server.URL + "/"},
		{"remote.origin.proxy", attacker.server.URL},
		{"http.proxy", attacker.server.URL},
		{"http.sslVerify", "false"},
		{"credential.helper", "store"},
		{"include.path", "/tmp/attacker.gitconfig"},
	} {
		for _, operation := range []string{"register", "detached"} {
			t.Run(operation+"/"+entry.key, func(t *testing.T) {
				m := NewManager(t.TempDir())
				trustFixtureCA(t, m, caFile)
				if _, err := m.RegisterWithCredential(t.Context(), remoteURL, readCredential()); err != nil {
					t.Fatal(err)
				}
				git(t, m.mirrorDir(remoteURL), "config", entry.key, entry.value)
				beforeFixture, _ := fixture.snapshot()
				beforeAttacker, _ := attacker.snapshot()
				var err error
				if operation == "register" {
					_, err = m.RegisterWithCredential(t.Context(), remoteURL, readCredential())
				} else {
					err = m.CreateDetachedWorkspaceWithCredential(t.Context(), remoteURL, strings.Repeat("a", 40), filepath.Join(t.TempDir(), "workspace"), readCredential())
				}
				refused := err != nil && (strings.Contains(err.Error(), "credential fetch refused") ||
					(operation == "register" && strings.HasPrefix(entry.key, "url.") && strings.Contains(err.Error(), "repository mirror origin mismatch")))
				if !refused {
					t.Fatalf("mirror config outside the allow-list accepted: %v", err)
				}
				if strings.Contains(err.Error(), entry.value) {
					t.Fatalf("refusal is not sanitized: %v", err)
				}
				if after, _ := fixture.snapshot(); after != beforeFixture {
					t.Fatal("refused fetch reached the expected host")
				}
				if after, sawToken := attacker.snapshot(); after != beforeAttacker || sawToken {
					t.Fatal("refused fetch reached the other endpoint")
				}
			})
		}
	}
}

func TestCredentialFetchInsteadOfNeverSendsTokenToRewrittenHost(t *testing.T) {
	requireHTTPSFixture(t)
	fixture := newHTTPSGitFixture(t)
	remoteURL, _ := newPrivateMirrorOrigin(t, fixture)
	attacker := newHTTPSGitFixture(t)
	newPrivateMirrorOrigin(t, attacker)
	m := NewManager(t.TempDir())
	trustFixtureCA(t, m, combinedCAFile(t, fixture, attacker))
	mirror, err := m.RegisterWithCredential(t.Context(), remoteURL, readCredential())
	if err != nil {
		t.Fatal(err)
	}
	git(t, mirror, "config", "url."+attacker.server.URL+"/.insteadOf", fixture.server.URL+"/")
	beforeFixture, _ := fixture.snapshot()
	_, err = m.runCredentialGit(t.Context(), mirror, remoteURL, *readCredential(), "fetch", "--prune", "origin")
	if err == nil {
		t.Fatal("fetch bypassing the config guard succeeded")
	}
	requests, sawToken := attacker.snapshot()
	if requests == 0 {
		t.Fatal("fixture precondition: insteadOf did not redirect the fetch")
	}
	if sawToken {
		t.Fatal("askpass answered the rewritten host with the token")
	}
	if after, _ := fixture.snapshot(); after != beforeFixture {
		t.Fatal("expected host was contacted although insteadOf rewrote the URL")
	}
	assertTokenAbsent(t, err)

	m2 := NewManager(t.TempDir())
	trustFixtureCA(t, m2, combinedCAFile(t, fixture, attacker))
	m2.testGitEnvironment = append(m2.testGitEnvironment,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url."+attacker.server.URL+"/.insteadOf",
		"GIT_CONFIG_VALUE_0="+fixture.server.URL+"/",
	)
	beforeAttacker, _ := attacker.snapshot()
	if _, err := m2.RegisterWithCredential(t.Context(), remoteURL, readCredential()); err == nil {
		t.Fatal("clone through an insteadOf rewrite succeeded")
	}
	if requests, sawToken := attacker.snapshot(); requests == beforeAttacker || sawToken {
		t.Fatalf("clone rewrite precondition or token leak: requests=%d token=%v", requests-beforeAttacker, sawToken)
	}
}

func TestCredentialCloneDoesNotFollowRedirectsWithToken(t *testing.T) {
	requireHTTPSFixture(t)
	attacker := newHTTPSGitFixture(t)
	newPrivateMirrorOrigin(t, attacker)
	redirecting := newHTTPSGitFixtureWithHandler(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, attacker.server.URL+request.URL.RequestURI(), http.StatusFound)
	}))
	root := t.TempDir()
	m := NewManager(root)
	trustFixtureCA(t, m, combinedCAFile(t, redirecting, attacker))
	_, err := m.RegisterWithCredential(t.Context(), redirecting.server.URL+"/origin.git", readCredential())
	if err == nil {
		t.Fatal("clone through a redirect succeeded")
	}
	if requests, sawToken := attacker.snapshot(); requests != 0 || sawToken {
		t.Fatalf("redirect target received requests=%d token=%v", requests, sawToken)
	}
	if _, sawToken := redirecting.snapshot(); sawToken {
		t.Fatal("redirecting host received the token")
	}
	assertTokenAbsent(t, err, root)
}
