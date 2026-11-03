package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/remoteconfig"
)

func runningLocalChecks() map[string]config.CheckConfig {
	return map[string]config.CheckConfig{
		"go-test": {Command: "go-owner-binary", Args: []string{"test", "--owner-secret-flag"}, TimeoutSeconds: 600, RepositoryID: "repo-1"},
		"lint":    {Command: "lint-owner-binary"},
	}
}

func TestReloadAppliesADocumentThatOmitsALocalCheckAndWarnsOnce(t *testing.T) {
	d, _, logs := reloadTestDaemon(t)
	d.cfg.Checks = runningLocalChecks()
	revoked := []string{strings.Repeat("1", 64)}
	omitting := hotReload(d.cfg, strings.Repeat("e", 64), func(c *config.Config) {
		c.Repositories = map[string]config.RepositoryConfig{"repo-2": {URL: "https://github.com/acme/web.git"}}
		c.Checks = map[string]config.CheckConfig{"lint": runningLocalChecks()["lint"]}
		c.RevokedAgentPackDigests = revoked
	})
	omitting.Merged.OmittedChecks = []remoteconfig.OmittedCheck{{ID: "go-test", RepositoryID: "repo-1"}}
	restoring := hotReload(d.cfg, strings.Repeat("f", 64), func(c *config.Config) { c.Checks = runningLocalChecks() })
	reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{omitting, restoring}, errs: []error{nil, nil}}
	d.configurationReloader = reloader
	if !d.reloadConfiguration(context.Background()) || reloader.commits() != 1 {
		t.Fatal("a document that omits a local check was not applied")
	}
	hot := d.hotConfiguration()
	if _, ok := hot.checks["go-test"]; ok || len(hot.checks) != 1 || !reflect.DeepEqual(hot.revokedAgentPackDigests, revoked) || len(hot.repositories) != 1 {
		t.Fatalf("hot = %+v", hot)
	}
	if count := strings.Count(logs.String(), "local check is not registered"); count != 1 || !strings.Contains(logs.String(), "go-test") || !strings.Contains(logs.String(), "repo-1") {
		t.Fatalf("logs = %s", logs.String())
	}
	if !d.reloadConfiguration(context.Background()) || reloader.commits() != 2 {
		t.Fatal("the restoring document was not applied")
	}
	if !reflect.DeepEqual(d.hotConfiguration().checks, runningLocalChecks()) {
		t.Fatalf("checks = %+v", d.hotConfiguration().checks)
	}
}

func countingCheck(t *testing.T) (config.CheckConfig, func() int) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "runs")
	check := config.CheckConfig{Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""}}
	return check, func() int {
		data, err := os.ReadFile(counter)
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "run")
	}
}

func applyChecksMidJob(t *testing.T, jr *jobRunner, checks map[string]config.CheckConfig) {
	t.Helper()
	next := jr.d.cfg
	next.Checks = checks
	if err := jr.d.applyHotConfiguration(next, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jr.d.hotConfiguration().checks, checks) {
		t.Fatalf("the daemon did not apply the check set %v", checks)
	}
}

func terminalCheckFailures(events []map[string]any) []string {
	var failures []string
	for _, event := range events {
		if event["phase"] != "terminal" {
			continue
		}
		if listed, ok := event["failures"].([]string); ok {
			failures = append(failures, listed...)
		}
	}
	return failures
}

func TestAJobKeepsTheChecksItStartedWith(t *testing.T) {
	t.Run("a check omitted mid-job still runs for the job that started with it", func(t *testing.T) {
		jr, cap := runCheckFixture(t)
		check, runs := countingCheck(t)
		jr.job.RequiredChecks = []string{"lint"}
		jr.d.cfg.Checks = map[string]config.CheckConfig{"lint": check}
		started := jr.d.hotConfiguration()
		jr.configuration = &started
		applyChecksMidJob(t, jr, map[string]config.CheckConfig{})
		if checks := jr.runnableChecks(); !slices.Equal(checks, []string{"lint"}) {
			t.Fatalf("runnable checks = %v", checks)
		}
		result, err := call(t, jr, "platform_run_check", `{"check_id":"lint","idempotency_key":"key-1"}`)
		if err != nil {
			t.Fatalf("the started check was refused after the omission: %v", err)
		}
		if result.(map[string]any)["result"] != "passed" || runs() != 1 {
			t.Fatalf("on-demand result = %v, runs = %d", result, runs())
		}
		commitSet := map[string]string{"primary": gitHead(t, jr.repositories[0].WorkDir)}
		if !jr.runRequiredChecks(context.Background(), commitSet, nil) {
			t.Fatalf("terminal checks failed: %v", terminalCheckFailures(cap.byKind("test_results")))
		}
		if runs() != 2 {
			t.Fatalf("terminal checks did not run the started check: %d runs", runs())
		}
		if failures := terminalCheckFailures(cap.byKind("test_results")); len(failures) != 0 {
			t.Fatalf("terminal failures = %v", failures)
		}
		jr.configuration = nil
		if checks := jr.runnableChecks(); len(checks) != 0 {
			t.Fatalf("a job started after the change sees %v", checks)
		}
		if _, err := call(t, jr, "platform_run_check", `{"check_id":"lint","idempotency_key":"key-2"}`); err == nil ||
			!strings.Contains(err.Error(), "not registered on this runner") {
			t.Fatalf("an omitted required check did not fail closed for a job started after the change: %v", err)
		}
	})
	t.Run("a check restored mid-job does not run for a job that started without it", func(t *testing.T) {
		jr, cap := runCheckFixture(t)
		check, runs := countingCheck(t)
		jr.job.RequiredChecks = []string{"lint"}
		jr.d.cfg.Checks = map[string]config.CheckConfig{}
		started := jr.d.hotConfiguration()
		jr.configuration = &started
		applyChecksMidJob(t, jr, map[string]config.CheckConfig{"lint": check})
		if checks := jr.runnableChecks(); len(checks) != 0 {
			t.Fatalf("runnable checks = %v", checks)
		}
		if _, err := call(t, jr, "platform_run_check", `{"check_id":"lint","idempotency_key":"key-1"}`); err == nil ||
			!strings.Contains(err.Error(), "not registered on this runner") {
			t.Fatalf("a check outside the start snapshot was accepted: %v", err)
		}
		commitSet := map[string]string{"primary": gitHead(t, jr.repositories[0].WorkDir)}
		if jr.runRequiredChecks(context.Background(), commitSet, nil) {
			t.Fatal("terminal checks passed with a check outside the start snapshot")
		}
		if runs() != 0 {
			t.Fatalf("a check outside the start snapshot ran %d times", runs())
		}
		if failures := terminalCheckFailures(cap.byKind("test_results")); !slices.Equal(failures, []string{"required check is not registered on this runner"}) {
			t.Fatalf("terminal failures = %v", failures)
		}
	})
}

func TestPublishedCapabilityNeverCarriesLocalCheckDefinitions(t *testing.T) {
	d, events, _ := reloadTestDaemon(t)
	d.cfg.Checks = runningLocalChecks()
	if err := d.publishCapability(context.Background()); err != nil {
		t.Fatal(err)
	}
	manifests := publishedManifests(t, events)
	if len(manifests) != 1 {
		t.Fatalf("manifests = %d", len(manifests))
	}
	raw, err := json.Marshal(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"go-owner-binary", "--owner-secret-flag", "go-test", "lint-owner-binary", "timeout_seconds"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("capability manifest carries %q: %s", leak, raw)
		}
	}
}
