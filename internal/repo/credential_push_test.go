package repo

import (
	"bytes"
	"encoding/base64"
	"encoding/pem"
	"io/fs"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const pushToken = "ghs_push000000000000000000000000000000000"

type httpsGitFixture struct {
	server     *httptest.Server
	projectDir string
	caFile     string

	mu               sync.Mutex
	requests         int
	authorized       int
	sawToken         bool
	tokenOutsideAuth bool
	anonymousRead    bool
}

func TestMain(m *testing.M) {
	if os.Getenv(AskPassModeEnv) == "1" {
		os.Exit(RunAskPass(os.Args[1:], os.Getenv, os.Stdout))
	}
	os.Exit(m.Run())
}

func newHTTPSGitFixture(t *testing.T) *httpsGitFixture {
	t.Helper()
	return newHTTPSGitFixtureWithHandler(t, nil)
}

func newHTTPSGitFixtureWithHandler(t *testing.T, override http.Handler) *httpsGitFixture {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skipf("git exec path unavailable: %v", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if info, err := os.Stat(backend); err != nil || info.IsDir() {
		t.Skipf("git http-backend is unavailable at %s; skipping the HTTPS push fixture", backend)
	}
	fixture := &httpsGitFixture{projectDir: t.TempDir()}
	handler := &cgi.Handler{
		Path: backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + fixture.projectDir,
			"GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=x-access-token",
		},
	}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.inspect(request)
		if override != nil {
			override.ServeHTTP(writer, request)
			return
		}
		fixture.mu.Lock()
		anonymousRead := fixture.anonymousRead
		fixture.mu.Unlock()
		if anonymousRead && (request.URL.Query().Get("service") == "git-upload-pack" ||
			strings.HasSuffix(request.URL.Path, "/git-upload-pack")) {
			handler.ServeHTTP(writer, request)
			return
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != "x-access-token" || password != pushToken {
			writer.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(writer, "authentication required", http.StatusUnauthorized)
			return
		}
		fixture.mu.Lock()
		fixture.authorized++
		fixture.mu.Unlock()
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(fixture.server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.server.Certificate().Raw})
	if err := os.WriteFile(caFile, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.caFile = caFile
	return fixture
}

func (fixture *httpsGitFixture) inspect(request *http.Request) {
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + pushToken))
	leaked := strings.Contains(request.URL.String(), pushToken) || strings.Contains(request.RequestURI, pushToken)
	seen := leaked
	for name, values := range request.Header {
		for _, value := range values {
			if strings.Contains(value, pushToken) || strings.Contains(value, encoded) {
				seen = true
				if name != "Authorization" {
					leaked = true
				}
			}
		}
	}
	fixture.mu.Lock()
	fixture.requests++
	fixture.sawToken = fixture.sawToken || seen
	fixture.tokenOutsideAuth = fixture.tokenOutsideAuth || leaked
	fixture.mu.Unlock()
}

func (fixture *httpsGitFixture) snapshot() (int, bool) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.requests, fixture.sawToken
}

func combinedCAFile(t *testing.T, fixtures ...*httpsGitFixture) string {
	t.Helper()
	var combined []byte
	for _, fixture := range fixtures {
		// #nosec G304 -- test reads files it created under t.TempDir.
		data, err := os.ReadFile(fixture.caFile)
		if err != nil {
			t.Fatal(err)
		}
		combined = append(combined, data...)
	}
	path := filepath.Join(t.TempDir(), "combined-ca.pem")
	if err := os.WriteFile(path, combined, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (fixture *httpsGitFixture) createBareRepository(t *testing.T, name string) string {
	t.Helper()
	bare := filepath.Join(fixture.projectDir, name)
	git(t, fixture.projectDir, "init", "--bare", "-b", "main", bare)
	git(t, bare, "config", "http.receivepack", "true")
	return bare
}

func prepareCredentialPushMirror(t *testing.T, m *Manager, remoteURL, caFile, branch string) {
	t.Helper()
	source := seedOrigin(t)
	git(t, source, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", ".")
	git(t, source, "commit", "-m", "feature")
	mirror := m.mirrorDir(remoteURL)
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, "", "clone", "--mirror", source, mirror)
	git(t, mirror, "remote", "set-url", "origin", remoteURL)
	trustFixtureCA(t, m, caFile)
}

func trustFixtureCA(t *testing.T, m *Manager, caFile string) {
	t.Helper()
	t.Setenv("SSL_CERT_FILE", caFile)
	m.testGitEnvironment = []string{"GIT_SSL_CAINFO=" + caFile}
}

func filesContaining(t *testing.T, root, needle string) []string {
	t.Helper()
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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
		if bytes.Contains(data, []byte(needle)) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestPushMirrorBranchWithCredentialOverHTTPS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HTTPS git fixture")
	}
	fixture := newHTTPSGitFixture(t)
	bare := fixture.createBareRepository(t, "origin.git")
	remoteURL := fixture.server.URL + "/origin.git"
	root := t.TempDir()
	m := NewManager(root)
	branch := "job/build-1/att-1"
	prepareCredentialPushMirror(t, m, remoteURL, fixture.caFile, branch)

	err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, branch, PushCredential{
		Username: "x-access-token", Password: pushToken,
	})
	if err != nil {
		t.Fatalf("credential push: %v", err)
	}
	git(t, bare, "rev-parse", "--verify", "refs/heads/"+branch)

	fixture.mu.Lock()
	authorized, leaked := fixture.authorized, fixture.tokenOutsideAuth
	fixture.mu.Unlock()
	if authorized == 0 {
		t.Fatal("fixture never saw an authorized request")
	}
	if leaked {
		t.Fatal("token reached the fixture outside the Authorization header")
	}
	if _, err := os.Stat(filepath.Join(root, "askpass")); !os.IsNotExist(err) {
		t.Fatalf("credential push wrote an askpass helper to disk: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(m.mirrorDir(remoteURL), "config"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(config, []byte(pushToken)) {
		t.Fatalf("mirror config holds the token:\n%s", config)
	}
	if matches := filesContaining(t, root, pushToken); len(matches) != 0 {
		t.Fatalf("token reached disk: %v", matches)
	}
	if matches := filesContaining(t, fixture.projectDir, pushToken); len(matches) != 0 {
		t.Fatalf("token reached the pushed repository: %v", matches)
	}
}

func TestPushMirrorBranchWithCredentialScrubsGitErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HTTPS git fixture")
	}
	fixture := newHTTPSGitFixture(t)
	remoteURL := fixture.server.URL + "/" + pushToken + ".git"
	root := t.TempDir()
	m := NewManager(root)
	branch := "job/build-1/att-1"
	prepareCredentialPushMirror(t, m, remoteURL, fixture.caFile, branch)

	err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, branch, PushCredential{
		Username: "x-access-token", Password: pushToken,
	})
	if err == nil {
		t.Fatal("push to a missing repository succeeded")
	}
	if strings.Contains(err.Error(), pushToken) {
		t.Fatalf("git error echoes the token: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("git error was not scrubbed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "askpass")); !os.IsNotExist(statErr) {
		t.Fatalf("credential push wrote an askpass helper to disk: %v", statErr)
	}
}

func TestCredentialPushCommandKeepsTokenOutOfArgv(t *testing.T) {
	m := NewManager(t.TempDir())
	helper := filepath.Join(t.TempDir(), "askpass.sh")
	remoteURL := "https://github.com/octo/example.git"
	cmd := m.credentialPushCommand(t.Context(), remoteURL, "job/one", helper, PushCredential{
		Username: "x-access-token", Password: pushToken,
	})
	for _, arg := range cmd.Args {
		if strings.Contains(arg, pushToken) {
			t.Fatalf("argv contains the token: %q", cmd.Args)
		}
	}
	for _, required := range []string{"credential.helper=", "core.askPass=" + helper, "http.followRedirects=false", "push", remoteURL} {
		if !slices.Contains(cmd.Args, required) {
			t.Fatalf("argv %q is missing %q", cmd.Args, required)
		}
	}
	helperReset := slices.Index(cmd.Args, "credential.helper=")
	if helperReset < 1 || cmd.Args[helperReset-1] != "-c" {
		t.Fatalf("credential helper reset is not a -c option: %q", cmd.Args)
	}
	for _, required := range []string{
		"GIT_ASKPASS=" + helper,
		"GIT_TERMINAL_PROMPT=0",
		"SHORTSTACK_GIT_USERNAME=x-access-token",
		"SHORTSTACK_GIT_PASSWORD=" + pushToken,
		"GIT_ALLOW_PROTOCOL=https",
		"SHORTSTACK_GIT_ASKPASS=1",
		"SHORTSTACK_GIT_HOST=github.com",
	} {
		if !slices.Contains(cmd.Env, required) {
			t.Fatalf("env is missing %q", required)
		}
	}
	if cmd.Dir != m.mirrorDir(remoteURL) {
		t.Fatalf("dir = %q", cmd.Dir)
	}
}

func TestPushMirrorBranchWithCredentialRequiresHTTPSOrigin(t *testing.T) {
	m := NewManager(t.TempDir())
	credential := PushCredential{Username: "x-access-token", Password: pushToken}
	for _, remoteURL := range []string{
		"ssh://git@github.com/octo/example.git",
		"https://user@github.com/octo/example.git",
		"/tmp/local.git",
	} {
		err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, "job/one", credential)
		if err == nil {
			t.Fatalf("credential push accepted %q", remoteURL)
		}
		if strings.Contains(err.Error(), pushToken) {
			t.Fatalf("error echoes the token: %v", err)
		}
	}
}

func TestRunAskPassAnswersOnlyTheExpectedHost(t *testing.T) {
	env := map[string]string{
		askPassHostEnv: "github.com", askPassUserEnv: "x-access-token", askPassSecretEnv: pushToken,
	}
	getenv := func(key string) string { return env[key] }
	cases := []struct {
		prompt string
		want   string
	}{
		{"Username for 'https://github.com': ", "x-access-token\n"},
		{"Password for 'https://x-access-token@github.com': ", pushToken + "\n"},
		{"Username for 'https://attacker.invalid': ", ""},
		{"Password for 'https://x-access-token@attacker.invalid': ", ""},
		{"Password for 'https://x-access-token@github.com.attacker.invalid': ", ""},
		{"Password for 'https://someone@github.com': ", ""},
		{"Password for 'https://github.com': ", ""},
		{"Enter passphrase for key '/home/runner/.ssh/id': ", ""},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		code := RunAskPass([]string{tc.prompt}, getenv, &out)
		if out.String() != tc.want {
			t.Fatalf("prompt %q answered %q", tc.prompt, out.String())
		}
		if (tc.want == "") != (code != 0) {
			t.Fatalf("prompt %q exit code %d", tc.prompt, code)
		}
	}
	var out bytes.Buffer
	if code := RunAskPass([]string{"Password for 'https://x-access-token@github.com': "}, func(string) string { return "" }, &out); code == 0 || out.Len() != 0 {
		t.Fatalf("askpass without expected host answered: %d %q", code, out.String())
	}
}

func TestCredentialPushRefusesMirrorConfigOutsideAllowList(t *testing.T) {
	fixture := newHTTPSGitFixture(t)
	fixture.createBareRepository(t, "origin.git")
	attacker := newHTTPSGitFixture(t)
	attacker.createBareRepository(t, "origin.git")
	remoteURL := fixture.server.URL + "/origin.git"
	fixtureHost := strings.TrimPrefix(fixture.server.URL, "https://")
	attackerHost := strings.TrimPrefix(attacker.server.URL, "https://")
	fixtureHostname, fixturePort, _ := strings.Cut(fixtureHost, ":")
	_, attackerPort, _ := strings.Cut(attackerHost, ":")
	caFile := combinedCAFile(t, fixture, attacker)
	for _, entry := range []struct{ key, value string }{
		{"http.curloptResolve", fixtureHostname + ":" + fixturePort + ":127.0.0.1:" + attackerPort},
		{"remote." + remoteURL + ".proxy", attacker.server.URL},
		{"remote." + remoteURL + ".url", attacker.server.URL + "/origin.git"},
		{"http.sslCAInfo", caFile},
		{"foo.bar", "attacker-value"},
		{"url." + attacker.server.URL + "/.insteadOf", fixture.server.URL + "/"},
		{"url." + attacker.server.URL + "/.pushInsteadOf", fixture.server.URL + "/"},
		{"include.path", "/tmp/attacker.gitconfig"},
		{"includeIf.gitdir:/.path", "/tmp/attacker.gitconfig"},
		{"http.sslVerify", "false"},
		{"http." + remoteURL + ".sslVerify", "false"},
		{"http.proxy", attacker.server.URL},
		{"remote.origin.pushurl", attacker.server.URL + "/origin.git"},
		{"remote.origin.proxy", attacker.server.URL},
		{"credential.helper", "store"},
	} {
		t.Run(entry.key, func(t *testing.T) {
			m := NewManager(t.TempDir())
			branch := "job/build-1/att-1"
			prepareCredentialPushMirror(t, m, remoteURL, caFile, branch)
			git(t, m.mirrorDir(remoteURL), "config", entry.key, entry.value)
			beforeFixture, _ := fixture.snapshot()
			beforeAttacker, _ := attacker.snapshot()
			err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, branch, PushCredential{
				Username: "x-access-token", Password: pushToken,
			})
			if err == nil || !strings.Contains(err.Error(), "credential push refused: mirror Git config sets ") {
				t.Fatalf("mirror config outside the allow-list accepted: %v", err)
			}
			if strings.Contains(err.Error(), pushToken) || strings.Contains(err.Error(), entry.value) {
				t.Fatalf("refusal is not sanitized: %v", err)
			}
			if after, _ := fixture.snapshot(); after != beforeFixture {
				t.Fatal("refused push reached the expected host")
			}
			if after, sawToken := attacker.snapshot(); after != beforeAttacker || sawToken {
				t.Fatal("refused push reached the other endpoint")
			}
		})
	}
}

func TestCredentialPushAllowListAcceptsProductionMirror(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HTTPS git fixture")
	}
	fixture := newHTTPSGitFixture(t)
	fixture.mu.Lock()
	fixture.anonymousRead = true
	fixture.mu.Unlock()
	source := seedOrigin(t)
	bare := filepath.Join(fixture.projectDir, "origin.git")
	git(t, "", "clone", "--bare", source, bare)
	git(t, bare, "config", "http.receivepack", "true")
	remoteURL := fixture.server.URL + "/origin.git"

	root := t.TempDir()
	m := NewManager(root)
	trustFixtureCA(t, m, fixture.caFile)
	mirror, err := m.Register(t.Context(), remoteURL)
	if err != nil {
		t.Fatalf("register mirror: %v", err)
	}
	if _, err := m.Register(t.Context(), remoteURL); err != nil {
		t.Fatalf("refresh mirror: %v", err)
	}

	branch := "job/build-1/att-1"
	workspace := filepath.Join(t.TempDir(), "workspace")
	m.AllowLocal = true
	if err := m.CreateWorkspace(t.Context(), remoteURL, branch, "main", workspace); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	m.AllowLocal = false
	git(t, workspace, "config", "user.email", "t@t")
	git(t, workspace, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, workspace, "add", ".")
	git(t, workspace, "commit", "-m", "feature")
	head, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "export.bundle")
	git(t, workspace, "bundle", "create", bundle, branch)
	if err := m.ImportBundle(t.Context(), remoteURL, bundle, branch, strings.TrimSpace(string(head))); err != nil {
		t.Fatalf("import bundle: %v", err)
	}

	if err := m.checkCredentialMirrorConfig(t.Context(), remoteURL, "push"); err != nil {
		t.Fatalf("production mirror failed the allow-list: %v", err)
	}
	if err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, branch, PushCredential{
		Username: "x-access-token", Password: pushToken,
	}); err != nil {
		t.Fatalf("credential push from production mirror: %v", err)
	}
	git(t, bare, "rev-parse", "--verify", "refs/heads/"+branch)
	if matches := filesContaining(t, mirror, pushToken); len(matches) != 0 {
		t.Fatalf("token reached the mirror: %v", matches)
	}
}

func TestSanitizedConfigKeyHidesSubsections(t *testing.T) {
	for input, want := range map[string]string{
		"foo.bar":             "foo.bar",
		"http.curloptResolve": "http.curloptresolve",
		"remote.https://x-access-token:secret@evil.invalid/.proxy": "remote.<subsection>.proxy",
		"url.https://evil.invalid/.pushInsteadOf":                  "url.<subsection>.pushinsteadof",
	} {
		if got := sanitizedConfigKey(input); got != want {
			t.Fatalf("sanitizedConfigKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCredentialPushInsteadOfNeverSendsTokenToRewrittenHost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HTTPS git fixture")
	}
	fixture := newHTTPSGitFixture(t)
	fixture.createBareRepository(t, "origin.git")
	attacker := newHTTPSGitFixture(t)
	attacker.createBareRepository(t, "origin.git")
	remoteURL := fixture.server.URL + "/origin.git"
	m := NewManager(t.TempDir())
	branch := "job/build-1/att-1"
	prepareCredentialPushMirror(t, m, remoteURL, combinedCAFile(t, fixture, attacker), branch)
	git(t, m.mirrorDir(remoteURL), "config", "url."+attacker.server.URL+"/.pushInsteadOf", fixture.server.URL+"/")
	credential := PushCredential{Username: "x-access-token", Password: pushToken}

	if err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, branch, credential); err == nil {
		t.Fatal("push with pushInsteadOf succeeded")
	}
	if requests, _ := attacker.snapshot(); requests != 0 {
		t.Fatalf("rewritten host received %d requests", requests)
	}

	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := m.credentialPushCommand(t.Context(), remoteURL, branch, helper, credential)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err == nil {
		t.Fatalf("push bypassing the config guard succeeded:\n%s", out.String())
	}
	requests, sawToken := attacker.snapshot()
	if requests == 0 {
		t.Fatal("fixture precondition: pushInsteadOf did not redirect the push")
	}
	if sawToken {
		t.Fatal("askpass answered the rewritten host with the token")
	}
	if _, fixtureSawToken := fixture.snapshot(); fixtureSawToken {
		t.Fatal("expected host saw the token although git never contacted it")
	}
}

func TestCredentialPushDoesNotFollowRedirectsWithToken(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HTTPS git fixture")
	}
	attacker := newHTTPSGitFixture(t)
	attacker.createBareRepository(t, "origin.git")
	redirecting := newHTTPSGitFixtureWithHandler(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, attacker.server.URL+request.URL.RequestURI(), http.StatusFound)
	}))
	remoteURL := redirecting.server.URL + "/origin.git"
	m := NewManager(t.TempDir())
	branch := "job/build-1/att-1"
	prepareCredentialPushMirror(t, m, remoteURL, combinedCAFile(t, redirecting, attacker), branch)

	err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, branch, PushCredential{
		Username: "x-access-token", Password: pushToken,
	})
	if err == nil {
		t.Fatal("push through a redirect succeeded")
	}
	if strings.Contains(err.Error(), pushToken) {
		t.Fatalf("error echoes the token: %v", err)
	}
	if requests, sawToken := attacker.snapshot(); requests != 0 || sawToken {
		t.Fatalf("redirect target received requests=%d token=%v", requests, sawToken)
	}
	if _, sawToken := redirecting.snapshot(); sawToken {
		t.Fatal("redirecting host received the token")
	}
}

func TestCredentialPushRefusesWorktreeAndIncludedConfig(t *testing.T) {
	fixture := newHTTPSGitFixture(t)
	fixture.createBareRepository(t, "origin.git")
	attacker := newHTTPSGitFixture(t)
	attacker.createBareRepository(t, "origin.git")
	remoteURL := fixture.server.URL + "/origin.git"
	fixtureHostname, fixturePort, _ := strings.Cut(strings.TrimPrefix(fixture.server.URL, "https://"), ":")
	_, attackerPort, _ := strings.Cut(strings.TrimPrefix(attacker.server.URL, "https://"), ":")
	resolve := fixtureHostname + ":" + fixturePort + ":127.0.0.1:" + attackerPort
	hostile := "[http]\n\tcurloptResolve = " + resolve + "\n"
	caFile := combinedCAFile(t, fixture, attacker)
	for name, arrange := range map[string]func(t *testing.T, mirror string){
		"worktree config with extension": func(t *testing.T, mirror string) {
			git(t, mirror, "config", "extensions.worktreeConfig", "true")
			if err := os.WriteFile(filepath.Join(mirror, "config.worktree"), []byte(hostile), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"worktree config file only": func(t *testing.T, mirror string) {
			if err := os.WriteFile(filepath.Join(mirror, "config.worktree"), []byte(hostile), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"worktree extension only": func(t *testing.T, mirror string) {
			git(t, mirror, "config", "extensions.worktreeConfig", "true")
		},
		"included file": func(t *testing.T, mirror string) {
			included := filepath.Join(t.TempDir(), "included.gitconfig")
			if err := os.WriteFile(included, []byte(hostile), 0o600); err != nil {
				t.Fatal(err)
			}
			git(t, mirror, "config", "include.path", included)
		},
		"conditionally included file": func(t *testing.T, mirror string) {
			included := filepath.Join(t.TempDir(), "included.gitconfig")
			if err := os.WriteFile(included, []byte(hostile), 0o600); err != nil {
				t.Fatal(err)
			}
			git(t, mirror, "config", "includeIf.gitdir:"+mirror+".path", included)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			branch := "job/build-1/att-1"
			prepareCredentialPushMirror(t, m, remoteURL, caFile, branch)
			arrange(t, m.mirrorDir(remoteURL))
			beforeFixture, _ := fixture.snapshot()
			beforeAttacker, _ := attacker.snapshot()
			err := m.PushMirrorBranchWithCredential(t.Context(), remoteURL, branch, PushCredential{
				Username: "x-access-token", Password: pushToken,
			})
			if err == nil || !strings.Contains(err.Error(), "credential push refused") {
				t.Fatalf("hostile config accepted: %v", err)
			}
			if strings.Contains(err.Error(), pushToken) || strings.Contains(err.Error(), resolve) {
				t.Fatalf("refusal is not sanitized: %v", err)
			}
			if after, _ := fixture.snapshot(); after != beforeFixture {
				t.Fatal("refused push reached the expected host")
			}
			if after, sawToken := attacker.snapshot(); after != beforeAttacker || sawToken {
				t.Fatal("refused push reached the other endpoint")
			}
		})
	}
}
