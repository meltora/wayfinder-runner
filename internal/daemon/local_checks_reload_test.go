package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
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

func TestAJobKeepsTheChecksItStartedWith(t *testing.T) {
	jr := workspaceValidationRunner(t, []v1.JobRepository{{RepositoryID: "repo-9", Primary: true}})
	jr.job.Kind = v1.JobKindBuild
	jr.job.RequiredChecks = []string{"lint"}
	jr.repositories = []checkedOutRepository{{ID: "repo-9", Primary: true}}
	jr.d.cfg.Checks = map[string]config.CheckConfig{"lint": {Command: "lint"}}
	started := jr.d.hotConfiguration()
	jr.configuration = &started
	if err := jr.d.applyHotConfiguration(func() config.Config {
		next := jr.d.cfg
		next.Checks = map[string]config.CheckConfig{}
		return next
	}(), strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if len(jr.d.hotConfiguration().checks) != 0 {
		t.Fatal("the daemon did not apply the narrower check set")
	}
	if checks := jr.runnableChecks(); !slices.Equal(checks, []string{"lint"}) {
		t.Fatalf("runnable checks = %v", checks)
	}
	jr.configuration = nil
	if checks := jr.runnableChecks(); len(checks) != 0 {
		t.Fatalf("a job started after the change sees %v", checks)
	}
	if _, err := jr.executeRunCheck(context.Background(), map[string]any{"check_id": "lint", "idempotency_key": "key-1"}); err == nil ||
		!strings.Contains(err.Error(), "not registered on this runner") {
		t.Fatalf("an omitted required check did not fail closed: %v", err)
	}
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
