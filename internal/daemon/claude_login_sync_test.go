package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
	"github.com/example/app-runner/internal/isolation"
)

func fakeOAuth(refresh string, expiresAt int64, account string) string {
	accountFields := ""
	if account != "" {
		accountFields = fmt.Sprintf(`,"accountUuid":%q,"organizationUuid":"org"`, account)
	}
	return fmt.Sprintf(`{"accessToken":"fake-access-%s","refreshToken":"fake-%s","expiresAt":%d,"scopes":["user:profile","user:inference"]%s}`, refresh, refresh, expiresAt, accountFields)
}

func fakeCredentials(oauth string) string {
	return `{"claudeAiOauth":` + oauth + `}`
}

type loginSyncFixture struct {
	t       *testing.T
	hostDir string
	sync    *claudeLoginSync
	now     time.Time
	logs    *bytes.Buffer
	calls   atomic.Int32
	homes   map[string]string
	writes  int
}

func newLoginSyncFixture(t *testing.T, hostOAuth string) *loginSyncFixture {
	t.Helper()
	hostDir := filepath.Join(t.TempDir(), "claude-personal")
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &loginSyncFixture{t: t, hostDir: hostDir, now: time.Unix(1_800_000_000, 0), logs: &bytes.Buffer{}, homes: map[string]string{}}
	f.setHost(hostOAuth)
	logger := slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.sync = newClaudeLoginSync(func(ctx context.Context, attemptID, home string, request claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error) {
		f.calls.Add(1)
		return claudelogin.Exchange(ctx, home, request, claudelogin.LockOptions{Retry: time.Millisecond, Timeout: 100 * time.Millisecond})
	}, nil, logger)
	f.sync.now = func() time.Time { return f.now }
	f.sync.lock = claudelogin.LockOptions{Retry: time.Millisecond, Timeout: 100 * time.Millisecond}
	return f
}

func (f *loginSyncFixture) setHost(oauth string) {
	path := filepath.Join(f.hostDir, ".credentials.json")
	writeTestFile(f.t, path, `{"mcpOAuth":{"server":{"accessToken":"fake-mcp"}},"claudeAiOauth":`+oauth+`}`)
	f.writes++
	future := time.Now().Add(time.Duration(f.writes) * time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		f.t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *loginSyncFixture) addJob(attemptID, jobOAuth, baselineRefresh string) *claudeLoginJob {
	f.t.Helper()
	home := filepath.Join(f.t.TempDir(), "home")
	writeTestFile(f.t, filepath.Join(home, ".claude", ".credentials.json"), fakeCredentials(jobOAuth))
	f.homes[attemptID] = home
	return f.sync.register(attemptID, isolation.JobHome{
		Path: home, ClaudeConfigDir: f.hostDir, ClaudeStateFile: filepath.Join(f.hostDir, ".claude.json"),
		Baseline: sha256.Sum256([]byte("fake-" + baselineRefresh)), SyncsLogin: true,
	}, nil)
}

func (f *loginSyncFixture) setJob(attemptID, jobOAuth string) {
	writeTestFile(f.t, filepath.Join(f.homes[attemptID], ".claude", ".credentials.json"), fakeCredentials(jobOAuth))
}

func refreshOf(t *testing.T, configDir string) string {
	t.Helper()
	_, login, err := claudelogin.ReadLogin(configDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"r0", "r1", "r5", "r9"} {
		if login.RefreshHash == sha256.Sum256([]byte("fake-"+name)) {
			return name
		}
	}
	return "unknown"
}

func (f *loginSyncFixture) jobRefresh(attemptID string) string {
	return refreshOf(f.t, filepath.Join(f.homes[attemptID], ".claude"))
}

func (f *loginSyncFixture) assertHost(want string) {
	f.t.Helper()
	if got := refreshOf(f.t, f.hostDir); got != want {
		f.t.Fatalf("host login = %s, want %s", got, want)
	}
	data, err := os.ReadFile(filepath.Join(f.hostDir, ".credentials.json"))
	if err != nil || !strings.Contains(string(data), `"mcpOAuth":{"server":{"accessToken":"fake-mcp"}}`) {
		f.t.Fatal("the host lost its other credentials")
	}
}

func (f *loginSyncFixture) assertNoTokenLogged() {
	f.t.Helper()
	if strings.Contains(f.logs.String(), "fake-") {
		f.t.Fatalf("a token reached the log: %s", f.logs.String())
	}
}

func TestClaudeLoginSyncHandsANewerHostLoginOnToEveryJob(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	for _, id := range []string{"a1", "a2", "a3"} {
		f.addJob(id, fakeOAuth("r0", 100, ""), "r0")
	}
	f.setHost(fakeOAuth("r1", 200, ""))
	f.sync.tick(context.Background())
	for _, id := range []string{"a1", "a2", "a3"} {
		if got := f.jobRefresh(id); got != "r1" {
			t.Fatalf("job %s login = %s, want r1", id, got)
		}
	}
	calls := f.calls.Load()
	f.now = f.now.Add(claudeLoginExchangeInterval)
	f.sync.tick(context.Background())
	if f.calls.Load() <= calls {
		t.Fatal("the periodic exchange did not run again")
	}
	f.assertHost("r1")
	if strings.Count(f.logs.String(), "claude login handed on") != 3 {
		t.Fatalf("hand-ons not logged once per job: %s", f.logs.String())
	}
	f.assertNoTokenLogged()
}

func TestClaudeLoginSyncNeverWritesAJobRotationBack(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.setJob("a1", fakeOAuth("r1", 200, ""))
	for range 2 {
		f.now = f.now.Add(claudeLoginExchangeInterval)
		f.sync.tick(context.Background())
	}
	f.assertHost("r0")
	if got := f.jobRefresh("a1"); got != "r1" {
		t.Fatalf("job login = %s, want its own r1", got)
	}
	if got := strings.Count(f.logs.String(), "event=job_login_rotated"); got != 1 {
		t.Fatalf("rotation warnings = %d, want 1: %s", got, f.logs.String())
	}
	f.assertNoTokenLogged()
}

func TestClaudeLoginSyncDoesNotHandOnAnotherAccountsLogin(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, "a"))
	f.addJob("a1", fakeOAuth("r0", 100, "a"), "r0")
	f.setHost(fakeOAuth("r9", 900, "b"))
	f.sync.tick(context.Background())
	if f.calls.Load() != 1 {
		t.Fatalf("a host change triggered %d exchanges, want 1", f.calls.Load())
	}
	if got := f.jobRefresh("a1"); got != "r0" {
		t.Fatalf("job login = %s, want r0", got)
	}
	f.assertNoTokenLogged()
}

func TestClaudeLoginSyncFinalCheckReportsARotationWithoutWritingIt(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	job := f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.setJob("a1", fakeOAuth("r1", 200, ""))
	job.stop()
	f.now = f.now.Add(claudeLoginExchangeInterval)
	f.sync.tick(context.Background())
	if f.calls.Load() != 0 {
		t.Fatal("a stopped job was exchanged")
	}
	job.final()
	job.final()
	f.assertHost("r0")
	if got := strings.Count(f.logs.String(), "event=job_login_rotated"); got != 1 {
		t.Fatalf("rotation warnings = %d, want 1", got)
	}
	f.now = f.now.Add(claudeLoginExchangeInterval)
	f.sync.tick(context.Background())
	if f.calls.Load() != 0 {
		t.Fatal("a finished job was exchanged")
	}
	f.assertNoTokenLogged()
}

func TestClaudeLoginSyncFinalCheckStaysInsideTheJobHome(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	job := f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	outside := filepath.Join(t.TempDir(), "outside")
	writeTestFile(t, filepath.Join(outside, ".credentials.json"), fakeCredentials(fakeOAuth("r1", 200, "")))
	jobConfig := filepath.Join(f.homes["a1"], ".claude")
	if err := os.RemoveAll(jobConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, jobConfig); err != nil {
		t.Fatal(err)
	}
	job.final()
	if !strings.Contains(f.logs.String(), "claude login final check skipped") || strings.Contains(f.logs.String(), "job_login_rotated") {
		t.Fatalf("the escaping link was followed: %s", f.logs.String())
	}
}

func TestClaudeLoginSyncStopWaitsForARunningExchange(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	job := f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	entered := make(chan struct{})
	release := make(chan struct{})
	f.sync.exchange = func(context.Context, string, string, claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error) {
		close(entered)
		<-release
		return claudelogin.ExchangeResult{Reason: claudelogin.ReasonUnchanged}, nil
	}
	f.now = f.now.Add(claudeLoginExchangeInterval)
	var wg sync.WaitGroup
	wg.Go(func() { f.sync.tick(context.Background()) })
	<-entered
	stopped := make(chan struct{})
	go func() {
		job.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop returned while an exchange was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-stopped
	wg.Wait()
}

func TestClaudeLoginSyncLogsAFailedExchangeWithoutValues(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.sync.exchange = func(context.Context, string, string, claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error) {
		return claudelogin.ExchangeResult{}, errors.New("the login exchange helper failed")
	}
	f.now = f.now.Add(claudeLoginExchangeInterval)
	f.sync.tick(context.Background())
	if !strings.Contains(f.logs.String(), "claude login exchange failed") {
		t.Fatalf("failure not logged: %s", f.logs.String())
	}
	f.assertHost("r0")
	f.assertNoTokenLogged()
}

func TestClaudeLoginSyncWithoutAHelperDoesNotExchange(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	f.sync.exchange = (&Daemon{}).claudeLoginExchanger()
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.setHost(fakeOAuth("r1", 200, ""))
	f.now = f.now.Add(claudeLoginExchangeInterval)
	f.sync.tick(context.Background())
	if got := f.jobRefresh("a1"); got != "r0" {
		t.Fatalf("job login = %s without a helper, want r0", got)
	}
}

func TestClaudeLoginSyncIgnoresHomesThatDoNotSync(t *testing.T) {
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	job := f.sync.register("a1", isolation.JobHome{Path: t.TempDir()}, nil)
	job.stop()
	job.final()
	var none *claudeLoginSync
	none.register("a2", isolation.JobHome{SyncsLogin: true}, nil).final()
	f.now = f.now.Add(claudeLoginExchangeInterval)
	f.sync.tick(context.Background())
	if f.calls.Load() != 0 {
		t.Fatal("a home without a file login was exchanged")
	}
}
