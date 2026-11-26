package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/isolation"
)

// Dependencies isolate local probes so setup diagnostics can be tested without contacting services.
type Dependencies struct {
	SystemChecks       func(context.Context, Options) []Check
	LookPath           func(string) (string, error)
	Stat               func(string) (os.FileInfo, error)
	PlatformTrustRoots func() (trustroot.Roots, error)
	ClaudeLogin        func([]string) (isolation.ClaudeLoginStatus, error)
	Now                func() time.Time
}

func Run(ctx context.Context, opts Options) Report {
	return RunWithDependencies(ctx, opts, Dependencies{
		SystemChecks: func(ctx context.Context, opts Options) []Check { return runSystemChecks(ctx, opts).Checks },
		LookPath:     exec.LookPath, Stat: os.Stat,
		PlatformTrustRoots: defaultPlatformTrustRoots(opts, os.Getenv(trustroot.OverrideEnvironment)),
		ClaudeLogin:        isolation.HostClaudeLogin, Now: time.Now,
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
	if check, ok := claudeLoginSyncCheck(opts, deps); ok {
		checks = append(checks, check)
	}
	checks = append(checks,
		registrationCheck("repository-registration", opts.RepositoryCount, "registered repositories", "repository access is not checked"),
		platformTrustCheck(opts, deps),
		isolationCheck(opts.Isolation),
		previewHostingCheck(opts),
		Check{Name: "local-check-limits", Status: Warn, Detail: "Local checks do not verify provider sign-in, runner assignment, or current app permissions. Job-image runtime availability is not checked."},
	)
	if opts.Isolation == "docker" {
		checks = append(checks, jobSocketPathCheck(opts.StateDir))
	}
	return Report{Checks: checks}
}

func jobSocketPathCheck(stateDir string) Check {
	if err := isolation.CheckJobSocketPath(stateDir); err != nil {
		return Check{Name: "job-socket-path", Status: Fail, Detail: err.Error()}
	}
	return Check{Name: "job-socket-path", Status: OK, Detail: fmt.Sprintf("the job MCP socket path is at most %d bytes", len(isolation.LongestJobSocketPath(stateDir)))}
}

func previewHostingCheck(opts Options) Check {
	if !opts.PreviewHostingEnabled {
		return Check{Name: "preview-hosting", Status: Skip, Detail: "disabled: preview_hosting.enabled is not set; run our-runner configure --preview-hosting on, or set it in the --config file"}
	}
	return Check{Name: "preview-hosting", Status: OK, Detail: fmt.Sprintf("enabled, at most %d previews at once; offered to the app once the preview tunnel is connected", opts.PreviewHostingMaxConcurrent)}
}

func isolationCheck(isolation string) Check {
	if isolation == "docker" {
		return Check{Name: "isolation", Status: OK, Detail: "container isolation"}
	}
	return Check{Name: "isolation", Status: Warn, Detail: "direct isolation is development-only: no container, the Agent Pack view relies on file permissions, and the host user's Claude configuration loads into jobs"}
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
	var parts, unapplied []string
	claudeConfigDirCopied := false
	for _, provider := range []string{"codex", "claude", "opencode"} {
		names := append([]string(nil), opts.ProviderEnvironmentNames[provider]...)
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		parts = append(parts, provider+": "+strings.Join(names, ", "))
		var skipped []string
		for _, name := range names {
			if provider == "claude" && name == "CLAUDE_CONFIG_DIR" {
				claudeConfigDirCopied = true
				continue
			}
			skipped = append(skipped, name)
		}
		if len(skipped) > 0 {
			unapplied = append(unapplied, provider+": "+strings.Join(skipped, ", "))
		}
	}
	if len(parts) == 0 {
		return Check{}, false
	}
	check := Check{Name: "provider-environment", Status: OK, Detail: strings.Join(parts, "; ") + "; applied to agent processes only; values are not shown"}
	if opts.Isolation != "docker" {
		return check, true
	}
	segments := append([]string(nil), parts...)
	if claudeConfigDirCopied {
		segments = append(segments, "under docker isolation the Claude login is copied from CLAUDE_CONFIG_DIR into the job home")
	}
	if len(unapplied) > 0 {
		check.Status = Warn
		segments = append(segments, "not applied under docker isolation: "+strings.Join(unapplied, "; "))
	}
	check.Detail = strings.Join(append(segments, "values are not shown"), "; ")
	return check, true
}
