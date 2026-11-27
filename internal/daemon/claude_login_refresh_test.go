package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
)

type fakeRefresher struct {
	calls  int
	tokens []string
	scopes [][]string
	out    func() ([]byte, error)
}

func (r *fakeRefresher) refresh(_ context.Context, token string, scopes []string) ([]byte, error) {
	r.calls++
	r.tokens = append(r.tokens, token)
	r.scopes = append(r.scopes, scopes)
	return r.out()
}

func newRefreshFixture(t *testing.T, hostExpiresIn time.Duration) (*loginSyncFixture, *fakeRefresher) {
	t.Helper()
	f := newLoginSyncFixture(t, fakeOAuth("r0", 100, ""))
	f.setHost(fakeOAuth("r0", f.now.Add(hostExpiresIn).UnixMilli(), ""))
	refresher := &fakeRefresher{out: func() ([]byte, error) {
		return []byte(fakeCredentials(fakeOAuth("r1", f.now.Add(8*time.Hour).UnixMilli(), ""))), nil
	}}
	f.sync.refresh = refresher.refresh
	return f, refresher
}

func TestClaudeLoginRefreshRunsOnceForThreeJobsAndHandsTheLoginOn(t *testing.T) {
	f, refresher := newRefreshFixture(t, 10*time.Minute)
	for _, id := range []string{"a1", "a2", "a3"} {
		f.addJob(id, fakeOAuth("r0", f.now.Add(10*time.Minute).UnixMilli(), ""), "r0")
	}
	f.sync.tick(context.Background())
	f.now = f.now.Add(claudeLoginHostCheckInterval)
	f.sync.tick(context.Background())
	if refresher.calls != 1 {
		t.Fatalf("refreshes = %d, want 1", refresher.calls)
	}
	if refresher.tokens[0] != "fake-r0" || !slices.Equal(refresher.scopes[0], []string{"user:profile", "user:inference"}) {
		t.Fatal("the refresh did not use the host grant")
	}
	f.assertHost("r1")
	for _, id := range []string{"a1", "a2", "a3"} {
		if got := f.jobRefresh(id); got != "r1" {
			t.Fatalf("job %s login = %s after the refresh, want r1", id, got)
		}
	}
	if !strings.Contains(f.logs.String(), "claude login refreshed") {
		t.Fatalf("refresh not logged: %s", f.logs.String())
	}
	f.assertNoTokenLogged()
}

func TestClaudeLoginRefreshRunsBeforeAJobCopiesAnExpiringLogin(t *testing.T) {
	f, refresher := newRefreshFixture(t, -time.Hour)
	f.sync.beforeStart(context.Background(), []string{"CLAUDE_CONFIG_DIR=" + f.hostDir})
	if refresher.calls != 1 {
		t.Fatalf("refreshes = %d before the first job, want 1", refresher.calls)
	}
	f.assertHost("r1")
	f.sync.beforeStart(context.Background(), []string{"CLAUDE_CONFIG_DIR=" + f.hostDir})
	if refresher.calls != 1 {
		t.Fatalf("refreshes = %d for a fresh login, want 1", refresher.calls)
	}
	var none *claudeLoginSync
	none.beforeStart(context.Background(), nil)
	f.assertNoTokenLogged()
}

func TestClaudeLoginRefreshWaitsUntilTheExpiryIsNear(t *testing.T) {
	f, refresher := newRefreshFixture(t, 20*time.Minute)
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.sync.tick(context.Background())
	if refresher.calls != 0 {
		t.Fatal("refreshed 20 minutes before expiry")
	}
	f.now = f.now.Add(5*time.Minute + time.Second)
	f.sync.tick(context.Background())
	if refresher.calls != 1 {
		t.Fatal("did not refresh within 15 minutes of expiry")
	}
}

func TestClaudeLoginRefreshSkipsLoginsWithoutAFileRefreshGrant(t *testing.T) {
	f, refresher := newRefreshFixture(t, time.Minute)
	f.sync.register("keychain", isolation.JobHome{Path: t.TempDir()}, nil)
	f.sync.tick(context.Background())
	if refresher.calls != 0 {
		t.Fatal("refreshed without a job that syncs a file login")
	}
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.setHost(`{"accessToken":"fake-access","expiresAt":` + "1" + `}`)
	f.sync.tick(context.Background())
	if refresher.calls != 0 {
		t.Fatal("refreshed a login without a refresh token")
	}
}

func TestClaudeLoginRefreshFailureIsLoggedWithoutValuesAndRetried(t *testing.T) {
	f, refresher := newRefreshFixture(t, 10*time.Minute)
	refresher.out = func() ([]byte, error) {
		return nil, errors.New("refresh the Claude login: exit status 1: claude login refresh failed: claude auth login exited with status 1")
	}
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.sync.tick(context.Background())
	f.now = f.now.Add(claudeLoginHostCheckInterval)
	f.sync.tick(context.Background())
	if refresher.calls != 1 {
		t.Fatalf("refreshes = %d before the retry delay, want 1", refresher.calls)
	}
	f.now = f.now.Add(claudeLoginRefreshRetry)
	f.sync.tick(context.Background())
	if refresher.calls != 2 {
		t.Fatalf("refreshes = %d after the retry delay, want 2", refresher.calls)
	}
	f.assertHost("r0")
	if !strings.Contains(f.logs.String(), "claude login refresh failed") {
		t.Fatalf("failure not logged: %s", f.logs.String())
	}
	f.assertNoTokenLogged()
}

func TestClaudeLoginRefreshRefusesAnUnusableResult(t *testing.T) {
	for name, test := range map[string]struct {
		out    func(f *loginSyncFixture) string
		reason string
	}{
		"older":            {out: func(f *loginSyncFixture) string { return fakeCredentials(fakeOAuth("r1", 100, "")) }, reason: "older"},
		"no refresh token": {out: func(*loginSyncFixture) string { return `{"claudeAiOauth":{"accessToken":"fake-a","expiresAt":9e15}}` }, reason: "unparsable"},
		"not json":         {out: func(*loginSyncFixture) string { return "fake-r1" }, reason: "unparsable"},
		"empty access token": {out: func(*loginSyncFixture) string {
			return `{"claudeAiOauth":{"accessToken":"","refreshToken":"fake-r1","expiresAt":9e15}}`
		}, reason: "unparsable"},
		"empty refresh token": {out: func(*loginSyncFixture) string {
			return `{"claudeAiOauth":{"accessToken":"fake-a","refreshToken":"","expiresAt":9e15}}`
		}, reason: "unparsable"},
		"no tokens": {out: func(*loginSyncFixture) string { return `{"claudeAiOauth":{}}` }, reason: "unparsable"},
		"another account": {out: func(f *loginSyncFixture) string {
			return fakeCredentials(fakeOAuth("r1", f.now.Add(8*time.Hour).UnixMilli(), "acct-other"))
		}, reason: "account_mismatch"},
	} {
		t.Run(name, func(t *testing.T) {
			f, refresher := newRefreshFixture(t, 10*time.Minute)
			f.setHost(fakeOAuth("r0", f.now.Add(10*time.Minute).UnixMilli(), "acct-owner"))
			refresher.out = func() ([]byte, error) { return []byte(test.out(f)), nil }
			f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
			f.sync.tick(context.Background())
			f.assertHost("r0")
			logs := f.logs.String()
			if !strings.Contains(logs, "reason="+test.reason) || !strings.Contains(logs, "host Claude login needs a new sign-in") ||
				!strings.Contains(logs, "event=host_login_needs_sign_in") {
				t.Fatalf("refusal not logged with %s and the sign-in notice: %s", test.reason, logs)
			}
			f.now = f.now.Add(claudeLoginRefreshRetry)
			f.sync.tick(context.Background())
			if refresher.calls != 1 {
				t.Fatalf("refreshes = %d, want no retry with a consumed grant", refresher.calls)
			}
			f.assertNoTokenLogged()
		})
	}
}

func TestClaudeLoginRefreshWritesAFarExpiryFromTheTrustedHelper(t *testing.T) {
	f, refresher := newRefreshFixture(t, 10*time.Minute)
	refresher.out = func() ([]byte, error) {
		return []byte(fakeCredentials(fakeOAuth("r1", f.now.Add(30*24*time.Hour).UnixMilli(), ""))), nil
	}
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.sync.tick(context.Background())
	f.assertHost("r1")
	if !strings.Contains(f.logs.String(), "reason=implausible_expiry") {
		t.Fatalf("far expiry not warned: %s", f.logs.String())
	}
	f.assertNoTokenLogged()
}

func TestClaudeLoginRefreshDoesNotHoldTheHostLockAndKeepsAConcurrentHostRefresh(t *testing.T) {
	f, refresher := newRefreshFixture(t, 10*time.Minute)
	refresher.out = func() ([]byte, error) {
		unlock, err := claudelogin.LockConfigDir(context.Background(), f.hostDir, claudelogin.LockOptions{Retry: time.Millisecond, Timeout: 50 * time.Millisecond})
		if err != nil {
			t.Errorf("the host lock was held during the refresh: %v", err)
			return nil, err
		}
		f.setHost(fakeOAuth("r5", f.now.Add(7*time.Hour).UnixMilli(), ""))
		if err := unlock(); err != nil {
			t.Error(err)
		}
		return []byte(fakeCredentials(fakeOAuth("r1", f.now.Add(8*time.Hour).UnixMilli(), ""))), nil
	}
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	f.sync.tick(context.Background())
	f.assertHost("r5")
	if !strings.Contains(f.logs.String(), "reason=host_changed") {
		t.Fatalf("dropped result not logged: %s", f.logs.String())
	}
	f.assertNoTokenLogged()
}

type fakeRefreshHelper struct {
	recordingOwnership
	requests []isolation.LoginRefresh
}

func (h *fakeRefreshHelper) RefreshLogin(_ context.Context, in isolation.LoginRefresh) ([]byte, error) {
	h.requests = append(h.requests, in)
	return []byte("{}"), nil
}

func TestClaudeLoginRefresherNeedsTheRestrictedJobNetwork(t *testing.T) {
	helper := &fakeRefreshHelper{}
	d := &Daemon{ownership: helper, runnerID: "runner-1", cfg: config.Config{Isolation: "docker", JobNetwork: "none"}}
	refresh := d.claudeLoginRefresher()
	if _, err := refresh(context.Background(), "fake-r0", []string{"user:inference"}); !errors.Is(err, errClaudeLoginRefreshUnavailable) {
		t.Fatalf("err = %v on network none", err)
	}
	d.cfg.JobNetwork, d.cfg.JobNetworkName = "restricted", "our-runner-jobs"
	if _, err := refresh(context.Background(), "fake-r0", []string{"user:inference"}); err != nil {
		t.Fatal(err)
	}
	if len(helper.requests) != 1 || helper.requests[0].Network != "our-runner-jobs" || helper.requests[0].RunnerID != "runner-1" {
		t.Fatalf("requests = %+v", helper.requests)
	}
	if (&Daemon{}).claudeLoginRefresher() != nil {
		t.Fatal("a refresher exists without the docker helper")
	}
}

func TestClaudeLoginRefreshUnavailableIsLoggedOnce(t *testing.T) {
	f, refresher := newRefreshFixture(t, 10*time.Minute)
	refresher.out = func() ([]byte, error) { return nil, errClaudeLoginRefreshUnavailable }
	f.addJob("a1", fakeOAuth("r0", 100, ""), "r0")
	for range 3 {
		f.sync.tick(context.Background())
		f.now = f.now.Add(claudeLoginRefreshRetry)
	}
	if got := strings.Count(f.logs.String(), "claude login refresh unavailable"); got != 1 {
		t.Fatalf("unavailable logged %d times", got)
	}
}
