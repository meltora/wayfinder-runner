package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Dependencies isolate local probes so setup diagnostics can be tested without contacting services.
type Dependencies struct {
	SystemChecks func(context.Context, Options) []Check
	LookPath     func(string) (string, error)
	Stat         func(string) (os.FileInfo, error)
}

func Run(ctx context.Context, opts Options) Report {
	return RunWithDependencies(ctx, opts, Dependencies{
		SystemChecks: func(ctx context.Context, opts Options) []Check { return runSystemChecks(ctx, opts).Checks },
		LookPath:     exec.LookPath, Stat: os.Stat,
	})
}

func RunWithDependencies(ctx context.Context, opts Options, deps Dependencies) Report {
	checks := append([]Check{}, deps.SystemChecks(ctx, opts)...)
	if !opts.ConfigurationLoaded {
		return Report{Checks: checks}
	}
	for index, check := range checks {
		if check.Name == "runner-certificate" && check.Status == Warn {
			checks[index] = Check{Name: check.Name, Status: Fail, Detail: "not enrolled; enroll this runner before starting configured work"}
		}
	}
	checks = append(checks, checkConfiguredRuntime(opts, deps))
	if check, ok := providerEnvironmentCheck(opts); ok {
		checks = append(checks, check)
	}
	checks = append(checks,
		registrationCheck("repository-registration", opts.RepositoryCount, "registered repositories", "repository access is not checked"),
		registrationCheck("agent-pack-trust", opts.TrustedAgentPackKeyCount, "trusted Pack keys", "Pack execution is not checked"),
		isolationCheck(opts.Isolation),
		awaitingAgentPackKeysCheck(opts),
		Check{Name: "local-check-limits", Status: Warn, Detail: "Local checks do not verify provider sign-in, runner assignment, or current app permissions. Job-image runtime availability is not checked."},
	)
	return Report{Checks: checks}
}

func isolationCheck(isolation string) Check {
	if isolation == "docker" {
		return Check{Name: "isolation", Status: OK, Detail: "container isolation"}
	}
	return Check{Name: "isolation", Status: Warn, Detail: "direct isolation is development-only: no container, the Agent Pack view relies on file permissions, and the host user's Claude configuration loads into jobs"}
}

func awaitingAgentPackKeysCheck(opts Options) Check {
	if opts.AgentPackKeysNotEvaluated != "" {
		return Check{Name: "agent-pack-keys-awaiting", Status: Warn, Detail: "Agent Pack keys awaiting acceptance were not evaluated: " + opts.AgentPackKeysNotEvaluated}
	}
	if len(opts.AwaitingAgentPackKeys) == 0 {
		return Check{Name: "agent-pack-keys-awaiting", Status: OK, Detail: "no Agent Pack keys are awaiting acceptance"}
	}
	keys := make([]string, 0, len(opts.AwaitingAgentPackKeys))
	for _, key := range opts.AwaitingAgentPackKeys {
		keys = append(keys, fmt.Sprintf("%s (sha256:%s)", key.ID, key.Fingerprint))
	}
	detail := "Agent Pack keys awaiting acceptance: " + strings.Join(keys, ", ")
	if opts.AcceptAgentPackKeysCommand != "" {
		detail += "; review the fingerprints, then run " + opts.AcceptAgentPackKeysCommand
	}
	return Check{Name: "agent-pack-keys-awaiting", Status: Warn, Detail: detail}
}

func checkConfiguredRuntime(opts Options, deps Dependencies) Check {
	check := Check{Name: "selected-runtime", Status: Fail, Detail: "selected runtime is unsupported"}
	if opts.Provider != "codex" && opts.Provider != "claude" && opts.Provider != "opencode" {
		return check
	}
	check.Name = "runtime-" + opts.Provider
	check.Detail = "selected runtime executable is unavailable; install it or check the configured binary"
	name := opts.Provider
	if opts.ProviderBinary != "" {
		if !filepath.IsAbs(opts.ProviderBinary) {
			return check
		}
		name = opts.ProviderBinary
	}
	path, err := deps.LookPath(name)
	if err != nil || !filepath.IsAbs(path) {
		return check
	}
	info, err := deps.Stat(path)
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return check
	}
	check.Status = OK
	check.Detail = "selected runtime executable is available locally; it was not run and provider sign-in is not checked"
	return check
}

func registrationCheck(name string, count int, label, limit string) Check {
	if count < 0 {
		return Check{Name: name, Status: Fail, Detail: "invalid configuration count"}
	}
	status := OK
	if count == 0 {
		status = Warn
	}
	return Check{Name: name, Status: status, Detail: fmt.Sprintf("%d %s configured; %s", count, label, limit)}
}

func providerEnvironmentCheck(opts Options) (Check, bool) {
	var parts []string
	for _, provider := range []string{"codex", "claude", "opencode"} {
		names := append([]string(nil), opts.ProviderEnvironmentNames[provider]...)
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		parts = append(parts, provider+": "+strings.Join(names, ", "))
	}
	if len(parts) == 0 {
		return Check{}, false
	}
	check := Check{Name: "provider-environment", Status: OK, Detail: strings.Join(parts, "; ") + "; applied to agent processes only; values are not shown"}
	if opts.Isolation == "docker" {
		check.Status = Warn
		check.Detail = strings.Join(parts, "; ") + "; not applied under docker isolation; values are not shown"
	}
	return check, true
}
