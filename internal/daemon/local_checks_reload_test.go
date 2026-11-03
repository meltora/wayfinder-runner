package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/remoteconfig"
)

func runningLocalChecks() map[string]config.CheckConfig {
	return map[string]config.CheckConfig{
		"go-test": {Command: "go-owner-binary", Args: []string{"test", "--owner-secret-flag"}, TimeoutSeconds: 600, RepositoryID: "repo-1"},
	}
}

func TestReloadKeepsLocalChecksAndRefusesADocumentThatChangesThem(t *testing.T) {
	d, _, _ := reloadTestDaemon(t)
	d.cfg.Checks = runningLocalChecks()
	reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("e", 64), func(c *config.Config) {
		c.Checks = runningLocalChecks()
	})}, errs: []error{nil}}
	d.configurationReloader = reloader
	if !d.reloadConfiguration(context.Background()) || reloader.commits() != 1 {
		t.Fatal("a hot document with unchanged local checks was not applied")
	}
	if !reflect.DeepEqual(d.cfg.Checks, runningLocalChecks()) {
		t.Fatalf("checks = %+v", d.cfg.Checks)
	}
	for name, change := range map[string]func(*config.Config){
		"removed": func(c *config.Config) { c.Checks = nil },
		"added":   func(c *config.Config) { c.Checks["lint"] = config.CheckConfig{Command: "lint"} },
		"replaced": func(c *config.Config) {
			c.Checks["go-test"] = config.CheckConfig{Command: "sh", Args: []string{"-c", "true"}}
		},
	} {
		d, events, _ := reloadTestDaemon(t)
		d.cfg.Checks = runningLocalChecks()
		before := d.hotConfiguration()
		reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, strings.Repeat("e", 64), func(c *config.Config) {
			c.Checks = runningLocalChecks()
			change(c)
		})}, errs: []error{nil}}
		d.configurationReloader = reloader
		if d.reloadConfiguration(context.Background()) {
			t.Fatalf("%s: a changed check set was applied without a restart", name)
		}
		if !reflect.DeepEqual(d.hotConfiguration(), before) || reloader.commits() != 0 || len(publishedManifests(t, events)) != 0 ||
			!reflect.DeepEqual(d.cfg.Checks, runningLocalChecks()) {
			t.Fatalf("%s: applied", name)
		}
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
	for _, leak := range []string{"go-owner-binary", "--owner-secret-flag", "go-test", "timeout_seconds"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("capability manifest carries %q: %s", leak, raw)
		}
	}
}
