package doctor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/isolation"
)

func claudeLoginDependencies(status isolation.ClaudeLoginStatus, err error, seen *[]string) Dependencies {
	deps := setupDependencies()
	deps.Now = func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) }
	deps.ClaudeLogin = func(environment []string) (isolation.ClaudeLoginStatus, error) {
		if seen != nil {
			*seen = environment
		}
		return status, err
	}
	return deps
}

func TestClaudeLoginSyncCheck(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	file := isolation.ClaudeLoginStatus{Source: isolation.ClaudeLoginFile, HasRefresh: true, AccountKnown: true, ExpiresAt: now.Add(42*time.Minute + 20*time.Second)}
	restricted := Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "docker", JobNetwork: "restricted", JobNetworkName: "our-runner-jobs"}
	for name, test := range map[string]struct {
		opts    Options
		status  isolation.ClaudeLoginStatus
		err     error
		want    Status
		details []string
	}{
		"synced file login": {
			opts: restricted, status: file, want: OK,
			details: []string{"file login", "sync applies", "scheduled refresh available", "account check available", "expires in 42 minutes"},
		},
		"no account ids and expired": {
			opts: restricted, status: isolation.ClaudeLoginStatus{Source: isolation.ClaudeLoginFile, HasRefresh: true, ExpiresAt: now.Add(-3 * time.Minute)},
			want: OK, details: []string{"account check unavailable", "expired 3 minutes ago"},
		},
		"no restricted network": {
			opts: Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "docker", JobNetwork: "none"}, status: file,
			want: Warn, details: []string{"sync applies", "scheduled refresh unavailable"},
		},
		"direct isolation": {
			opts: Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "direct"}, status: file,
			want: Warn, details: []string{"not synced", "docker isolation"},
		},
		"no refresh token": {
			opts: restricted, status: isolation.ClaudeLoginStatus{Source: isolation.ClaudeLoginFile, ExpiresAt: now.Add(time.Hour)},
			want: Warn, details: []string{"no refresh token", "not synced"},
		},
		"keychain":   {opts: restricted, status: isolation.ClaudeLoginStatus{Source: isolation.ClaudeLoginKeychain}, want: Warn, details: []string{"keychain login", "not synced"}},
		"no login":   {opts: restricted, status: isolation.ClaudeLoginStatus{Source: isolation.ClaudeLoginNone}, want: Warn, details: []string{"no Claude file login"}},
		"unreadable": {opts: restricted, err: errors.New("the Claude login in .credentials.json is unparsable"), want: Warn, details: []string{"unparsable"}},
		"api key only": {
			opts: Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "docker", ClaudeAPIKey: true}, status: isolation.ClaudeLoginStatus{Source: isolation.ClaudeLoginNone},
			want: Skip, details: []string{"API key"},
		},
		"api key and a file login": {
			opts: Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "docker", JobNetwork: "restricted", JobNetworkName: "jobs", ClaudeAPIKey: true}, status: file,
			want: OK, details: []string{"sync applies"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			check := findCheck(t, RunWithDependencies(context.Background(), test.opts, claudeLoginDependencies(test.status, test.err, nil)), "claude-login-sync")
			if check.Status != test.want {
				t.Fatalf("status = %v, want %v: %s", check.Status, test.want, check.Detail)
			}
			for _, detail := range test.details {
				if !strings.Contains(check.Detail, detail) {
					t.Fatalf("detail %q lacks %q", check.Detail, detail)
				}
			}
		})
	}
}

func TestClaudeLoginSyncCheckOnlyForClaude(t *testing.T) {
	t.Parallel()
	report := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "codex"}, claudeLoginDependencies(isolation.ClaudeLoginStatus{}, nil, nil))
	for _, check := range report.Checks {
		if check.Name == "claude-login-sync" {
			t.Fatal("a Claude login check for a codex runner")
		}
	}
	var seen []string
	opts := Options{ConfigurationLoaded: true, Provider: "codex", WorkIntakeProposalEnabled: true, WorkIntakeProposalAdapter: "claude", ClaudeConfigDir: "/srv/claude"}
	findCheck(t, RunWithDependencies(context.Background(), opts, claudeLoginDependencies(isolation.ClaudeLoginStatus{Source: isolation.ClaudeLoginNone}, nil, &seen)), "claude-login-sync")
	if !slices.Equal(seen, []string{"CLAUDE_CONFIG_DIR=/srv/claude"}) {
		t.Fatalf("environment = %v", seen)
	}
}
