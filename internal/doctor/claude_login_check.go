package doctor

import (
	"fmt"
	"strings"
	"time"

	"github.com/example/app-runner/internal/isolation"
)

func usesClaude(opts Options) bool {
	return opts.Provider == "claude" || (opts.WorkIntakeProposalEnabled && opts.WorkIntakeProposalAdapter == "claude")
}

func claudeLoginSyncCheck(opts Options, deps Dependencies) (Check, bool) {
	if !usesClaude(opts) || deps.ClaudeLogin == nil {
		return Check{}, false
	}
	check := Check{Name: "claude-login-sync", Status: Warn}
	var environment []string
	if opts.ClaudeConfigDir != "" {
		environment = []string{"CLAUDE_CONFIG_DIR=" + opts.ClaudeConfigDir}
	}
	status, err := deps.ClaudeLogin(environment)
	switch {
	case opts.ClaudeAPIKey && (err != nil || status.Source != isolation.ClaudeLoginFile):
		check.Status, check.Detail = Skip, "API key sign-in and no Claude file login; there is no login to sync"
		return check, true
	case err != nil:
		check.Detail = err.Error()
		return check, true
	case status.Source == isolation.ClaudeLoginKeychain:
		check.Detail = "keychain login; not synced, so a refresh inside a job can end the host login; use a file login with CLAUDE_CONFIG_DIR"
		return check, true
	case status.Source != isolation.ClaudeLoginFile:
		check.Detail = "no Claude file login found; sign in with claude on this host or set CLAUDE_CONFIG_DIR"
		return check, true
	case !status.HasRefresh:
		check.Detail = "file login with no refresh token; not synced"
		return check, true
	case opts.Isolation != "docker":
		check.Detail = "file login; not synced: the sync needs docker isolation"
		return check, true
	}
	parts := []string{"file login", "sync applies"}
	if opts.JobNetwork == "restricted" && opts.JobNetworkName != "" {
		check.Status = OK
		parts = append(parts, "scheduled refresh available")
	} else {
		parts = append(parts, "scheduled refresh unavailable: it needs job_network restricted")
	}
	if status.AccountKnown {
		parts = append(parts, "account check available")
	} else {
		parts = append(parts, "account check unavailable: the login carries no account ids")
	}
	now := time.Now
	if deps.Now != nil {
		now = deps.Now
	}
	minutes := int(status.ExpiresAt.Sub(now()).Round(time.Minute) / time.Minute)
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("access token expires in %d minutes", minutes))
	} else {
		parts = append(parts, fmt.Sprintf("access token expired %d minutes ago", -minutes))
	}
	check.Detail = strings.Join(parts, "; ")
	return check, true
}
