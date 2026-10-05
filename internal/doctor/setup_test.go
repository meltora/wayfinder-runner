package doctor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type executableInfo struct{ mode os.FileMode }

func (f executableInfo) Name() string       { return "runtime" }
func (f executableInfo) Size() int64        { return 1 }
func (f executableInfo) Mode() os.FileMode  { return f.mode }
func (f executableInfo) ModTime() time.Time { return time.Time{} }
func (f executableInfo) IsDir() bool        { return f.mode.IsDir() }
func (f executableInfo) Sys() any           { return nil }
func setupDependencies() Dependencies {
	return Dependencies{
		SystemChecks: func(context.Context, Options) []Check {
			return []Check{{Name: "existing-system-check", Status: OK, Detail: "unchanged"}, {Name: "runner-certificate", Status: OK, Detail: "enrolled"}}
		},

		LookPath: func(string) (string, error) { return "/trusted/bin/runtime", nil },
		Stat:     func(string) (os.FileInfo, error) { return executableInfo{0o755}, nil },
	}
}
func findCheck(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing check %s", name)
	return Check{}
}
func TestConfiguredSetupReportsLocalFactsWithoutAuthorityClaims(t *testing.T) {
	t.Parallel()
	report := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "codex", RepositoryCount: 2, TrustedAgentPackKeyCount: 3}, setupDependencies())
	if report.ExitCode() != 0 {
		t.Fatalf("unexpected setup failure: %s", Format(report))
	}
	for _, name := range []string{"runtime-codex", "repository-registration", "agent-pack-trust", "local-check-limits"} {
		findCheck(t, report, name)
	}
	if !strings.Contains(findCheck(t, report, "repository-registration").Detail, "2 registered") || !strings.Contains(findCheck(t, report, "agent-pack-trust").Detail, "3 trusted") {
		t.Fatal("configured counts missing")
	}
	text := Format(report)
	for _, expected := range []string{"access is not checked", "execution is not checked", "provider sign-in", "runner assignment", "current app permissions"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing limitation %s", expected)
		}
	}
	if strings.Contains(text, "/trusted/") {
		t.Fatal("runtime path disclosed")
	}
}
func TestConfiguredSetupRequiresIdentityButLimitedDiagnosticsKeepWarning(t *testing.T) {
	t.Parallel()
	deps := setupDependencies()
	deps.SystemChecks = func(context.Context, Options) []Check {
		return []Check{{Name: "existing-system-check", Status: OK}, {Name: "runner-certificate", Status: Warn, Detail: "not enrolled"}}
	}
	full := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "codex"}, deps)
	if full.ExitCode() != 1 || findCheck(t, full, "runner-certificate").Status != Fail {
		t.Fatal("missing configured identity did not fail")
	}
	deps.LookPath = func(string) (string, error) { t.Fatal("limited diagnostics inspected a runtime"); return "", nil }
	limited := RunWithDependencies(context.Background(), Options{}, deps)
	if limited.ExitCode() != 0 || findCheck(t, limited, "runner-certificate").Status != Warn || len(limited.Checks) != 2 {
		t.Fatal("limited diagnostics changed")
	}
}
func TestConfiguredSetupUsesExactProviderBinaryWithoutExecutingIt(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"codex", "claude", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			deps := setupDependencies()
			deps.LookPath = func(value string) (string, error) {
				if value != "/private/operator/runtime" {
					t.Fatal("configured binary was not selected")
				}
				return value, nil
			}
			report := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: provider, ProviderBinary: "/private/operator/runtime"}, deps)
			if findCheck(t, report, "runtime-"+provider).Status != OK {
				t.Fatal("installed runtime rejected")
			}
			if strings.Contains(Format(report), "/private/operator") {
				t.Fatal("configured path disclosed")
			}
		})
	}
}
func TestConfiguredSetupFailsUnavailableRuntimeWithSafeDetails(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "relative-result", "stat-error", "directory", "not-executable", "unsupported-provider", "relative-config"} {
		t.Run(kind, func(t *testing.T) {
			deps := setupDependencies()
			opts := Options{ConfigurationLoaded: true, Provider: "codex"}
			switch kind {
			case "missing":
				deps.LookPath = func(string) (string, error) { return "", errors.New("secret-provider-path") }
			case "relative-result":
				deps.LookPath = func(string) (string, error) { return "relative-secret-path", nil }
			case "stat-error":
				deps.Stat = func(string) (os.FileInfo, error) { return nil, errors.New("secret-stat-path") }
			case "directory":
				deps.Stat = func(string) (os.FileInfo, error) { return executableInfo{os.ModeDir | 0o755}, nil }
			case "not-executable":
				deps.Stat = func(string) (os.FileInfo, error) { return executableInfo{0o644}, nil }
			case "unsupported-provider":
				opts.Provider = "private-secret-provider"
			case "relative-config":
				opts.ProviderBinary = "relative-secret-path"
			}
			report := RunWithDependencies(context.Background(), opts, deps)
			if report.ExitCode() != 1 {
				t.Fatal("unavailable runtime did not fail")
			}
			if strings.Contains(Format(report), "secret") {
				t.Fatal("runtime error disclosed private input")
			}
		})
	}
}
func TestConfiguredSetupReportsEmptyAndInvalidCounts(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, -1} {
		t.Run(string(rune('a'-count)), func(t *testing.T) {
			report := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "codex", RepositoryCount: count, TrustedAgentPackKeyCount: count}, setupDependencies())
			want := Warn
			if count < 0 {
				want = Fail
			}
			if findCheck(t, report, "repository-registration").Status != want || findCheck(t, report, "agent-pack-trust").Status != want {
				t.Fatal("empty or invalid count misreported")
			}
		})
	}
}
func TestConfiguredSetupDoesNotMutateInjectedChecks(t *testing.T) {
	t.Parallel()
	original := []Check{{Name: "original", Status: OK}}
	deps := setupDependencies()
	deps.SystemChecks = func(context.Context, Options) []Check { return original }
	report := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "codex"}, deps)
	report.Checks[0].Status = Fail
	if original[0].Status != OK {
		t.Fatal("caller checks mutated")
	}
}
func TestConfiguredSetupReportsProviderEnvironmentNamesOnly(t *testing.T) {
	t.Parallel()
	opts := Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "direct", ProviderEnvironmentNames: map[string][]string{
		"claude": {"ANTHROPIC_LOG", "CLAUDE_CONFIG_DIR"}, "codex": {"CODEX_HOME"}, "opencode": nil,
	}}
	report := RunWithDependencies(context.Background(), opts, setupDependencies())
	check := findCheck(t, report, "provider-environment")
	if check.Status != OK || check.Detail != "codex: CODEX_HOME; claude: ANTHROPIC_LOG, CLAUDE_CONFIG_DIR; applied to agent processes only; values are not shown" {
		t.Fatalf("check = %+v", check)
	}
	opts.Isolation = "docker"
	docker := findCheck(t, RunWithDependencies(context.Background(), opts, setupDependencies()), "provider-environment")
	if docker.Status != Warn || !strings.Contains(docker.Detail, "not applied under docker isolation") || !strings.Contains(docker.Detail, "CLAUDE_CONFIG_DIR") {
		t.Fatalf("docker check = %+v", docker)
	}
	for _, c := range RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "claude"}, setupDependencies()).Checks {
		if c.Name == "provider-environment" {
			t.Fatal("provider environment reported without configuration")
		}
	}
}
func TestConfiguredSetupReportsIsolationMode(t *testing.T) {
	t.Parallel()
	docker := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "docker"}, setupDependencies())
	if check := findCheck(t, docker, "isolation"); check.Status != OK || check.Detail != "container isolation" {
		t.Fatalf("docker isolation = %+v", check)
	}
	direct := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "claude", Isolation: "direct"}, setupDependencies())
	check := findCheck(t, direct, "isolation")
	want := "direct isolation is development-only: no container, the Agent Pack view relies on file permissions, and the host user's Claude configuration loads into jobs"
	if check.Status != Warn || check.Detail != want {
		t.Fatalf("direct isolation = %+v", check)
	}
	if direct.ExitCode() != 0 {
		t.Fatalf("a Warn changed the exit code: %s", Format(direct))
	}
	if !strings.Contains(Format(direct), "[WARN] isolation: "+want) {
		t.Fatalf("formatted report = %s", Format(direct))
	}
}
func TestConfiguredSetupReportsAgentPackKeysAwaitingAcceptance(t *testing.T) {
	t.Parallel()
	clean := RunWithDependencies(context.Background(), Options{ConfigurationLoaded: true, Provider: "claude"}, setupDependencies())
	if check := findCheck(t, clean, "agent-pack-keys-awaiting"); check.Status != OK {
		t.Fatalf("no awaiting keys = %+v", check)
	}
	opts := Options{
		ConfigurationLoaded: true, Provider: "claude",
		AwaitingAgentPackKeys: []AwaitingAgentPackKey{
			{ID: "platform-key-2", Fingerprint: strings.Repeat("ab", 32)},
			{ID: "org-key-1", Fingerprint: strings.Repeat("cd", 32)},
		},
		AcceptAgentPackKeysCommand: "our-runner configure --state-dir '/runner state' --accept-pack-keys",
	}
	report := RunWithDependencies(context.Background(), opts, setupDependencies())
	check := findCheck(t, report, "agent-pack-keys-awaiting")
	if check.Status != Warn {
		t.Fatalf("awaiting keys = %+v", check)
	}
	for _, expected := range []string{
		"platform-key-2 (sha256:" + strings.Repeat("ab", 32) + ")",
		"org-key-1 (sha256:" + strings.Repeat("cd", 32) + ")",
		"our-runner configure --state-dir '/runner state' --accept-pack-keys",
	} {
		if !strings.Contains(check.Detail, expected) {
			t.Fatalf("detail lacks %q: %s", expected, check.Detail)
		}
	}
	if report.ExitCode() != 0 {
		t.Fatalf("awaiting keys changed the exit code: %s", Format(report))
	}
}

func TestConfiguredSetupReportsAgentPackKeysThatCouldNotBeEvaluated(t *testing.T) {
	t.Parallel()
	opts := Options{ConfigurationLoaded: true, Provider: "claude", AgentPackKeysNotEvaluated: "the state directory is not enrolled"}
	report := RunWithDependencies(context.Background(), opts, setupDependencies())
	check := findCheck(t, report, "agent-pack-keys-awaiting")
	if check.Status != Warn || check.Detail != "Agent Pack keys awaiting acceptance were not evaluated: the state directory is not enrolled" {
		t.Fatalf("not evaluated = %+v", check)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("not evaluated changed the exit code: %s", Format(report))
	}
}
