package claudelogin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	exchangeJobLogin   = `{"claudeAiOauth":{"accessToken":"job-access","refreshToken":"job-refresh-1","expiresAt":100}}`
	exchangeHostNewer  = `{"accessToken":"host-access","refreshToken":"host-refresh-2","expiresAt":200}`
	exchangeHostOlder  = `{"accessToken":"host-access","refreshToken":"host-refresh-2","expiresAt":50}`
	exchangeHostSame   = `{"accessToken":"job-access","refreshToken":"job-refresh-1","expiresAt":100}`
	exchangeJobRenewed = `{"claudeAiOauth":{"accessToken":"job-access-2","refreshToken":"job-refresh-3","expiresAt":300}}`
)

var exchangeBaseline = sha256.Sum256([]byte("job-refresh-1"))

func exchangeHome(t *testing.T, credentials string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if credentials != "" {
		if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(credentials), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func readJobCredentials(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exchangeOptions() LockOptions {
	return LockOptions{Retry: 5 * time.Millisecond, Timeout: 200 * time.Millisecond}
}

func TestExchangeHandsANewerHostLoginToAJobOnItsBaseline(t *testing.T) {
	home := exchangeHome(t, exchangeJobLogin)
	result, err := Exchange(context.Background(), home, ExchangeRequest{Host: json.RawMessage(exchangeHostNewer), Baseline: exchangeBaseline}, exchangeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != ReasonOK || !result.Replaced {
		t.Fatalf("result = %s replaced=%v", result.Reason, result.Replaced)
	}
	if !strings.Contains(string(result.Job), "job-refresh-1") {
		t.Fatal("the result does not carry the job login from before the write")
	}
	after, err := ParseCredentials([]byte(readJobCredentials(t, home)))
	if err != nil {
		t.Fatal(err)
	}
	if after.RefreshHash != sha256.Sum256([]byte("host-refresh-2")) || after.ExpiresAt != 200 {
		t.Fatal("the job copy was not replaced by the host login")
	}
	info, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".oauth_refresh.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the exchange left its lock behind")
	}
}

func TestExchangeLeavesTheJobCopyAloneUnlessTheDecisionIsOK(t *testing.T) {
	cases := []struct {
		name, job, host, claudeJSON string
		want                        Reason
	}{
		{name: "host on the baseline", job: exchangeJobLogin, host: exchangeHostSame, want: ReasonUnchanged},
		{name: "host older", job: exchangeJobLogin, host: exchangeHostOlder, want: ReasonOlder},
		{name: "job refreshed", job: exchangeJobRenewed, host: `{"accessToken":"h","refreshToken":"host-refresh-4","expiresAt":400}`, want: ReasonHostChanged},
		{name: "job refreshed past the host", job: exchangeJobRenewed, host: exchangeHostNewer, want: ReasonOlder},
		{name: "other account", job: `{"claudeAiOauth":{"accessToken":"j","refreshToken":"job-refresh-1","expiresAt":100,"accountUuid":"a","organizationUuid":"o"}}`,
			host: `{"accessToken":"h","refreshToken":"host-refresh-2","expiresAt":200,"accountUuid":"b","organizationUuid":"o"}`, want: ReasonAccountMismatch},
		{name: "other account in state", job: exchangeJobLogin, host: exchangeHostNewer,
			claudeJSON: `{"oauthAccount":{"accountUuid":"job","organizationUuid":"o"}}`, want: ReasonAccountMismatch},
		{name: "job copy unparsable", job: `{"claudeAiOauth":`, host: exchangeHostNewer, want: ReasonUnparsable},
		{name: "job copy missing", host: exchangeHostNewer, want: ReasonUnparsable},
		{name: "job state unparsable", job: exchangeJobLogin, host: exchangeHostNewer, claudeJSON: `{`, want: ReasonUnparsable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := exchangeHome(t, tc.job)
			if tc.claudeJSON != "" {
				if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(tc.claudeJSON), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			request := ExchangeRequest{Host: json.RawMessage(tc.host), HostAccount: Account{AccountUUID: "host", OrganizationUUID: "o"}, Baseline: exchangeBaseline}
			if tc.claudeJSON == "" {
				request.HostAccount = Account{}
			}
			result, err := Exchange(context.Background(), home, request, exchangeOptions())
			if err != nil {
				t.Fatal(err)
			}
			if result.Reason != tc.want || result.Replaced {
				t.Fatalf("result = %s replaced=%v, want %s", result.Reason, result.Replaced, tc.want)
			}
			if tc.job != "" && readJobCredentials(t, home) != tc.job {
				t.Fatal("the job copy changed")
			}
		})
	}
}

func TestExchangeReturnsTheRefreshedJobLogin(t *testing.T) {
	home := exchangeHome(t, exchangeJobRenewed)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"a","organizationUuid":"o"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Exchange(context.Background(), home, ExchangeRequest{Host: json.RawMessage(exchangeHostNewer), Baseline: exchangeBaseline}, exchangeOptions())
	if err != nil {
		t.Fatal(err)
	}
	login, err := ParseCredentials([]byte(`{"claudeAiOauth":` + string(result.Job) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if login.RefreshHash != sha256.Sum256([]byte("job-refresh-3")) || result.JobAccount != (Account{AccountUUID: "a", OrganizationUUID: "o"}) {
		t.Fatalf("job login = %+v", result)
	}
}

func TestExchangeRejectsAnUnusableHostLogin(t *testing.T) {
	home := exchangeHome(t, exchangeJobLogin)
	for _, host := range []string{"", `{"refreshToken":"x"}`, `[]`, `{"accessToken":"a","expiresAt":1} {}`} {
		if _, err := Exchange(context.Background(), home, ExchangeRequest{Host: json.RawMessage(host), Baseline: exchangeBaseline}, exchangeOptions()); err == nil {
			t.Fatalf("host %q was accepted", host)
		}
	}
	if readJobCredentials(t, home) != exchangeJobLogin {
		t.Fatal("the job copy changed")
	}
}

func TestExchangeWaitsForClaudeCodesLock(t *testing.T) {
	home := exchangeHome(t, exchangeJobLogin)
	if err := os.Mkdir(filepath.Join(home, ".claude", ".oauth_refresh.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Exchange(context.Background(), home, ExchangeRequest{Host: json.RawMessage(exchangeHostNewer), Baseline: exchangeBaseline}, exchangeOptions())
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if readJobCredentials(t, home) != exchangeJobLogin {
		t.Fatal("the job copy changed while Claude Code held the lock")
	}
}

func TestExchangeDoesNotFollowACredentialsSymlinkOutOfTheHome(t *testing.T) {
	home := exchangeHome(t, "")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(exchangeJobLogin), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".claude", ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	result, err := Exchange(context.Background(), home, ExchangeRequest{Host: json.RawMessage(exchangeHostNewer), Baseline: exchangeBaseline}, exchangeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != ReasonUnparsable || result.Replaced {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != exchangeJobLogin {
		t.Fatal("a file outside the home changed")
	}
}

func TestExchangeMessagesRoundTripAsJSON(t *testing.T) {
	request := ExchangeRequest{Host: json.RawMessage(exchangeHostNewer), HostAccount: Account{AccountUUID: "a", OrganizationUUID: "o"}, Baseline: exchangeBaseline}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExchangeRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Baseline != request.Baseline || decoded.HostAccount != request.HostAccount || string(decoded.Host) != exchangeHostNewer {
		t.Fatalf("decoded = %+v", decoded)
	}
	if _, err := DecodeExchangeRequest([]byte(`{"host":{},"extra":1}`)); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if _, err := DecodeExchangeRequest([]byte(strings.Repeat(" ", MaxExchangeBytes+1))); err == nil {
		t.Fatal("an oversize request was accepted")
	}
	result := ExchangeResult{Job: json.RawMessage(exchangeHostSame), JobAccount: Account{AccountUUID: "a"}, Replaced: true, Reason: ReasonOK}
	data, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeExchangeResult(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Reason != ReasonOK || !back.Replaced || back.JobAccount != result.JobAccount || string(back.Job) != exchangeHostSame {
		t.Fatalf("decoded result = %+v", back)
	}
}
