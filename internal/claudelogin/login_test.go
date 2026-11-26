package claudelogin

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
)

func credentials(t *testing.T, refresh string, expiresAt int64, account *Account) []byte {
	t.Helper()
	oauth := map[string]any{
		"accessToken":      "access-" + refresh,
		"expiresAt":        expiresAt,
		"scopes":           []string{"user:inference", "user:profile"},
		"subscriptionType": "max",
	}
	if refresh != "" {
		oauth["refreshToken"] = refresh
	}
	if account != nil {
		oauth["accountUuid"] = account.AccountUUID
		oauth["organizationUuid"] = account.OrganizationUUID
	}
	data, err := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parsed(t *testing.T, refresh string, expiresAt int64, account *Account) Login {
	t.Helper()
	login, err := ParseCredentials(credentials(t, refresh, expiresAt, account))
	if err != nil {
		t.Fatal(err)
	}
	return login
}

func TestParseCredentials(t *testing.T) {
	login := parsed(t, "fake-refresh-a", 1795713992053, &Account{AccountUUID: "acc-1", OrganizationUUID: "org-1"})
	if !login.HasRefresh || login.RefreshHash != sha256.Sum256([]byte("fake-refresh-a")) {
		t.Fatalf("refresh hash not recorded")
	}
	if login.ExpiresAt != 1795713992053 {
		t.Fatalf("expiresAt = %d", login.ExpiresAt)
	}
	if login.Account != (Account{AccountUUID: "acc-1", OrganizationUUID: "org-1"}) {
		t.Fatalf("account = %+v", login.Account)
	}
	if !json.Valid(login.OAuth) || !strings.Contains(string(login.OAuth), "subscriptionType") {
		t.Fatalf("oauth object not kept raw")
	}
	withoutRefresh := parsed(t, "", 1795713992053, nil)
	if withoutRefresh.HasRefresh || withoutRefresh.Account.Known() {
		t.Fatalf("unexpected refresh or account: %+v", withoutRefresh)
	}
}

func TestParseCredentialsRejects(t *testing.T) {
	for name, data := range map[string]string{
		"truncated":         `{"claudeAiOauth":{"accessToken":"x","expiresAt":1`,
		"missing oauth":     `{"mcpOAuth":{}}`,
		"oauth not object":  `{"claudeAiOauth":"x"}`,
		"no access token":   `{"claudeAiOauth":{"expiresAt":1795713992053}}`,
		"expiresAt string":  `{"claudeAiOauth":{"accessToken":"x","expiresAt":"soon"}}`,
		"expiresAt missing": `{"claudeAiOauth":{"accessToken":"x"}}`,
		"not an object":     `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCredentials([]byte(data))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), `"x"`) {
				t.Fatalf("error leaks content: %v", err)
			}
		})
	}
}

func TestParseCredentialsErrorDoesNotLeakToken(t *testing.T) {
	_, err := ParseCredentials([]byte(`{"claudeAiOauth":{"accessToken":"secret-access","refreshToken":"secret-refresh","expiresAt":"bad"}}`))
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseAccount(t *testing.T) {
	account, err := ParseAccount([]byte(`{"numStartups":3,"oauthAccount":{"accountUuid":"acc-1","organizationUuid":"org-1","emailAddress":"someone@example.invalid"}}`))
	if err != nil || account != (Account{AccountUUID: "acc-1", OrganizationUUID: "org-1"}) {
		t.Fatalf("account = %+v, err = %v", account, err)
	}
	none, err := ParseAccount([]byte(`{"numStartups":3}`))
	if err != nil || none.Known() {
		t.Fatalf("account = %+v, err = %v", none, err)
	}
	if _, err := ParseAccount([]byte(`{"oauthAccount":`)); err == nil {
		t.Fatal("expected an error for truncated state")
	}
}

func TestAccountKnown(t *testing.T) {
	if (Account{AccountUUID: "a"}).Known() || (Account{OrganizationUUID: "o"}).Known() || !(Account{AccountUUID: "a", OrganizationUUID: "o"}).Known() {
		t.Fatal("Known must require both ids")
	}
}

func TestDecide(t *testing.T) {
	same := &Account{AccountUUID: "acc-1", OrganizationUUID: "org-1"}
	other := &Account{AccountUUID: "acc-2", OrganizationUUID: "org-1"}
	otherOrg := &Account{AccountUUID: "acc-1", OrganizationUUID: "org-2"}
	stateSame := Account{AccountUUID: "acc-1", OrganizationUUID: "org-1"}
	stateOther := Account{AccountUUID: "acc-9", OrganizationUUID: "org-9"}
	baseline := sha256.Sum256([]byte("r0"))
	cases := []struct {
		name             string
		candidate        Login
		host             Login
		candidateAccount Account
		hostAccount      Account
		want             Reason
	}{
		{"refreshed same account in state", parsed(t, "r1", 2000, nil), parsed(t, "r0", 1000, nil), stateSame, stateSame, ReasonOK},
		{"refreshed same account in oauth", parsed(t, "r1", 2000, same), parsed(t, "r0", 1000, same), Account{}, Account{}, ReasonOK},
		{"oauth account wins over state", parsed(t, "r1", 2000, same), parsed(t, "r0", 1000, same), stateOther, stateSame, ReasonOK},
		{"oauth on one side falls back to state", parsed(t, "r1", 2000, same), parsed(t, "r0", 1000, nil), stateSame, stateSame, ReasonOK},
		{"no account anywhere", parsed(t, "r1", 2000, nil), parsed(t, "r0", 1000, nil), Account{}, Account{}, ReasonOK},
		{"unchanged refresh token", parsed(t, "r0", 2000, nil), parsed(t, "r0", 1000, nil), stateSame, stateSame, ReasonUnchanged},
		{"no refresh token", parsed(t, "", 2000, nil), parsed(t, "r0", 1000, nil), stateSame, stateSame, ReasonUnchanged},
		{"older", parsed(t, "r1", 900, nil), parsed(t, "r0", 1000, nil), stateSame, stateSame, ReasonOlder},
		{"equal expiry", parsed(t, "r1", 1000, nil), parsed(t, "r0", 1000, nil), stateSame, stateSame, ReasonOlder},
		{"host changed by someone else", parsed(t, "r1", 2000, nil), parsed(t, "r2", 1500, nil), stateSame, stateSame, ReasonHostChanged},
		{"other account in oauth", parsed(t, "r1", 2000, other), parsed(t, "r0", 1000, same), stateSame, stateSame, ReasonAccountMismatch},
		{"other organization in oauth", parsed(t, "r1", 2000, otherOrg), parsed(t, "r0", 1000, same), stateSame, stateSame, ReasonAccountMismatch},
		{"other account in state", parsed(t, "r1", 2000, nil), parsed(t, "r0", 1000, nil), stateOther, stateSame, ReasonAccountMismatch},
		{"account known on candidate only", parsed(t, "r1", 2000, same), parsed(t, "r0", 1000, nil), Account{}, Account{}, ReasonAccountMismatch},
		{"state known on host only", parsed(t, "r1", 2000, nil), parsed(t, "r0", 1000, nil), Account{}, stateSame, ReasonAccountMismatch},
		{"unparsable candidate", Login{}, parsed(t, "r0", 1000, nil), stateSame, stateSame, ReasonUnparsable},
		{"unparsable host", parsed(t, "r1", 2000, nil), Login{}, stateSame, stateSame, ReasonUnparsable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.candidate, tc.host, baseline, tc.candidateAccount, tc.hostAccount); got != tc.want {
				t.Fatalf("Decide = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMergeCredentialsKeepsOtherKeys(t *testing.T) {
	host := []byte(`{"mcpOAuth":{"server|1":{"accessToken":"mcp-secret"}},"claudeAiOauth":{"accessToken":"old","expiresAt":1},"futureKey":[1,2,3]}`)
	merged, err := MergeCredentials(host, json.RawMessage(`{"accessToken":"new","expiresAt":2}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mcpOAuth":{"server|1":{"accessToken":"mcp-secret"}},"claudeAiOauth":{"accessToken":"new","expiresAt":2},"futureKey":[1,2,3]}`
	if string(merged) != want {
		t.Fatalf("merged = %s", merged)
	}
}

func TestMergeCredentialsAddsMissingOAuth(t *testing.T) {
	merged, err := MergeCredentials([]byte(`{"mcpOAuth":{}}`), json.RawMessage(`{"accessToken":"new","expiresAt":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != `{"mcpOAuth":{},"claudeAiOauth":{"accessToken":"new","expiresAt":2}}` {
		t.Fatalf("merged = %s", merged)
	}
}

func TestMergeCredentialsRejects(t *testing.T) {
	if _, err := MergeCredentials([]byte(`{"mcpOAuth":`), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error for a truncated target")
	}
	if _, err := MergeCredentials([]byte(`{}`), json.RawMessage(`{"accessToken":`)); err == nil {
		t.Fatal("expected an error for a truncated oauth object")
	}
	if _, err := MergeCredentials([]byte(`{}`), json.RawMessage(`"x"`)); err == nil {
		t.Fatal("expected an error for a non-object oauth value")
	}
}

func TestJobCredentialsDropsOtherKeys(t *testing.T) {
	job, err := JobCredentials([]byte(`{"mcpOAuth":{"server|1":{"accessToken":"mcp-secret"}},"claudeAiOauth":{"accessToken":"a","expiresAt":1},"other":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(job) != `{"claudeAiOauth":{"accessToken":"a","expiresAt":1}}` {
		t.Fatalf("job = %s", job)
	}
	if _, err := JobCredentials([]byte(`{"mcpOAuth":{}}`)); err == nil {
		t.Fatal("expected an error without claudeAiOauth")
	}
}
