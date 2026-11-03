package remoteconfig

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

func localChecks() map[string]config.CheckConfig {
	return map[string]config.CheckConfig{
		"go-test": {Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 600},
		"web-unit": {Command: "npm", Args: []string{"test"}, TimeoutSeconds: 900, RepositoryID: "repo-1",
			Install: &config.CheckInstallConfig{Ecosystem: "npm", Directories: []string{"web"}, TimeoutSeconds: 300}},
	}
}

func deliveredChecks(ids ...string) map[string]config.CheckConfig {
	all := config.Normalize(config.Config{Checks: localChecks()}).Checks
	out := make(map[string]config.CheckConfig, len(ids))
	for _, id := range ids {
		out[id] = all[id]
	}
	return out
}

const localChecksYAML = `checks:
  go-test:
    command: go
    args: [test, ./...]
    timeout_seconds: 600
  web-unit:
    command: npm
    args: [test]
    timeout_seconds: 900
    repository_id: repo-1
    install:
      ecosystem: npm
      directories: [web]
      timeout_seconds: 300
`

func writeLocalFile(t *testing.T, local Local, extra string) {
	t.Helper()
	if err := os.MkdirAll(local.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := "control_plane_url: " + local.ControlPlaneURL + "\nstate_dir: " + local.StateDir + "\n"
	if err := os.WriteFile(filepath.Join(local.StateDir, LocalFileName), []byte(base+extra), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMergeTakesChecksOnlyFromTheLocalFile(t *testing.T) {
	local := testLocal(t)
	local.Checks = localChecks()
	merged, err := Merge(local, parsed(t, deliveryBody(dockerConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged.Config.Checks, deliveredChecks("go-test", "web-unit")) || len(merged.OmittedChecks) != 0 {
		t.Fatalf("checks = %+v omitted = %+v", merged.Config.Checks, merged.OmittedChecks)
	}
	install := merged.Config.Checks["web-unit"].Install
	if install == nil || install.Ecosystem != "npm" || !reflect.DeepEqual(install.Directories, []string{"web"}) || install.TimeoutSeconds != 300 {
		t.Fatalf("install = %+v", install)
	}
	merged.Config.Checks["go-test"].Args[0] = "changed"
	install.Directories[0] = "changed"
	if local.Checks["go-test"].Args[0] != "test" || local.Checks["web-unit"].Install.Directories[0] != "web" {
		t.Fatal("merged checks alias the local file")
	}
	merged, err = Merge(testLocal(t), parsed(t, deliveryBody(dockerConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Config.Checks) != 0 {
		t.Fatalf("checks without a local definition = %+v", merged.Config.Checks)
	}
}

func TestMergeOmitsALocalCheckWhoseRepositoryIsNotDelivered(t *testing.T) {
	local := testLocal(t)
	local.AcknowledgeDirect = true
	local.Checks = localChecks()
	merged, err := Merge(local, parsed(t, deliveryBody(directConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged.Config.Checks, deliveredChecks("go-test")) {
		t.Fatalf("checks = %+v", merged.Config.Checks)
	}
	if !reflect.DeepEqual(merged.OmittedChecks, []OmittedCheck{{ID: "web-unit", RepositoryID: "repo-1"}}) {
		t.Fatalf("omitted = %+v", merged.OmittedChecks)
	}
	if len(local.Checks) != 2 {
		t.Fatal("omission changed the local checks")
	}
}

func TestMergeValidatesLocalChecksLikeAConfigFile(t *testing.T) {
	for name, check := range map[string]config.CheckConfig{
		"empty command":           {Command: ""},
		"long timeout":            {Command: "go", TimeoutSeconds: 1801},
		"negative timeout":        {Command: "go", TimeoutSeconds: -1},
		"removed dependency dirs": {Command: "go", DependencyDirectories: []string{"node_modules"}},
		"escaping install dir":    {Command: "npm", Install: &config.CheckInstallConfig{Ecosystem: "npm", Directories: []string{"../outside"}}},
		"unsupported ecosystem":   {Command: "pip", Install: &config.CheckInstallConfig{Ecosystem: "pip"}},
		"too many args":           {Command: "go", Args: make([]string, 129)},
		"oversized command name":  {Command: strings.Repeat("g", 513)},
	} {
		base, err := Merge(testLocal(t), parsed(t, deliveryBody(dockerConfig())))
		if err != nil {
			t.Fatal(err)
		}
		direct := base.Config
		direct.Checks = map[string]config.CheckConfig{"go-test": check}
		want := direct.Validate()
		if want == nil {
			t.Fatalf("%s: config validation accepted the check", name)
		}
		local := testLocal(t)
		local.Checks = map[string]config.CheckConfig{"go-test": check}
		if _, err := Merge(local, parsed(t, deliveryBody(dockerConfig()))); err == nil || !strings.Contains(err.Error(), want.Error()) {
			t.Fatalf("%s: merge err = %v, config err = %v", name, err, want)
		}
	}
	local := testLocal(t)
	local.Checks = map[string]config.CheckConfig{"bad id!": {Command: "go"}}
	if _, err := Merge(local, parsed(t, deliveryBody(dockerConfig()))); err == nil || !strings.Contains(err.Error(), "check ID") {
		t.Fatalf("invalid check ID err = %v", err)
	}
	local.Checks = map[string]config.CheckConfig{"go-test": {Command: "", RepositoryID: "repo-gone"}}
	if _, err := Merge(local, parsed(t, deliveryBody(dockerConfig()))); err == nil || !strings.Contains(err.Error(), "check go-test has an invalid command") {
		t.Fatalf("an invalid check escaped validation by omission: %v", err)
	}
}

func TestLoadLocalReadsChecksStrictlyAndSaveKeepsThem(t *testing.T) {
	local := testLocal(t)
	writeLocalFile(t, local, localChecksYAML)
	loaded, err := LoadLocal(local.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Checks, localChecks()) {
		t.Fatalf("checks = %+v", loaded.Checks)
	}
	if err := SaveFiles(local.StateDir, loaded, []byte(`{"cached":true}`)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadLocal(local.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Checks, localChecks()) {
		t.Fatalf("checks after save = %+v", reloaded.Checks)
	}
	for name, extra := range map[string]string{
		"unknown check key":   "checks:\n  go-test:\n    command: go\n    retries: 3\n",
		"unknown install key": "checks:\n  go-test:\n    command: npm\n    install:\n      ecosystem: npm\n      scripts: true\n",
		"env on a check":      "checks:\n  go-test:\n    command: go\n    env: {GOFLAGS: -x}\n",
		"list of checks":      "checks:\n  - command: go\n",
	} {
		writeLocalFile(t, local, extra)
		if _, err := LoadLocal(local.StateDir); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestTheRemoteDocumentCannotCarryChecks(t *testing.T) {
	for name, value := range map[string]any{
		"empty map":  map[string]any{},
		"definition": map[string]any{"go-test": map[string]any{"command": "sh", "args": []any{"-c", "curl attacker"}}},
		"null":       nil,
	} {
		remote := dockerConfig()
		remote["checks"] = value
		if _, err := ParseDelivery(encode(t, deliveryBody(remote)), testRunnerID); err == nil {
			t.Fatalf("%s: a remote checks key was accepted", name)
		}
	}
	local := testLocal(t)
	local.Checks = localChecks()
	running := deliveryBody(dockerConfig())
	next := dockerConfig()
	next["checks"] = map[string]any{"go-test": map[string]any{"command": "sh"}}
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("e", 64)))}}}
	reloader := runningReloader(t, local, running, script.fetch)
	reload, err := reloader.Check(context.Background())
	if err == nil || reload.Outcome != ReloadUnchanged {
		t.Fatalf("a hot document with checks: reload = %+v err = %v", reload, err)
	}
}

func TestReloaderKeepsTheStartTimeLocalChecksUntilARestart(t *testing.T) {
	local := testLocal(t)
	writeLocalFile(t, local, localChecksYAML)
	if err := SaveCachedDelivery(local.StateDir, encode(t, deliveryBody(dockerConfig()))); err != nil {
		t.Fatal(err)
	}
	var gateways []string
	started, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(dockerConfig())), nil, &gateways), noSleepPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(started.Merged.Config.Checks, deliveredChecks("go-test", "web-unit")) {
		t.Fatalf("start checks = %+v", started.Merged.Config.Checks)
	}
	writeLocalFile(t, local, "checks:\n  lint:\n    command: golangci-lint\n")
	next := dockerConfig()
	next["labels"] = map[string]any{"pool": "us-linux"}
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("e", 64)))}}}
	reloader := NewReloader(local.StateDir, testRunnerID, started.Local, started.Delivery, script.fetch)
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	if !reflect.DeepEqual(reload.Merged.Config.Checks, deliveredChecks("go-test", "web-unit")) || reload.Merged.Config.Labels["pool"] != "us-linux" {
		t.Fatalf("hot reload checks = %+v", reload.Merged.Config.Checks)
	}
	restarted, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(dockerConfig())), nil, &gateways), noSleepPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restarted.Merged.Config.Checks, map[string]config.CheckConfig{"lint": {Command: "golangci-lint"}}) {
		t.Fatalf("restart checks = %+v", restarted.Merged.Config.Checks)
	}
}

func TestReloaderAppliesADocumentThatDropsTheRepositoryOfALocalCheck(t *testing.T) {
	local := testLocal(t)
	local.Checks = localChecks()
	dropped := dockerConfig()
	dropped["repositories"] = map[string]any{"repo-2": map[string]any{"url": "https://github.com/acme/web.git"}}
	dropped["revoked_agent_pack_digests"] = revokedTestDigests(1)
	dropped["labels"] = map[string]any{"pool": "us-linux"}
	restored := dockerConfig()
	script := &conditionalScript{responses: []conditionalResponse{
		{raw: encode(t, bodyWithDigest(dropped, strings.Repeat("e", 64)))},
		{raw: encode(t, bodyWithDigest(restored, strings.Repeat("f", 64)))},
	}}
	reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	cfg := reload.Merged.Config
	if !reflect.DeepEqual(cfg.Checks, deliveredChecks("go-test")) ||
		!reflect.DeepEqual(reload.Merged.OmittedChecks, []OmittedCheck{{ID: "web-unit", RepositoryID: "repo-1"}}) {
		t.Fatalf("checks = %+v omitted = %+v", cfg.Checks, reload.Merged.OmittedChecks)
	}
	if len(cfg.RevokedAgentPackDigests) != 1 || cfg.Labels["pool"] != "us-linux" || len(cfg.Repositories) != 1 {
		t.Fatalf("the rest of the document was not applied: %+v", cfg)
	}
	if err := reloader.Commit(reload); err != nil {
		t.Fatal(err)
	}
	reload, err = reloader.Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply {
		t.Fatalf("restore reload = %+v err = %v", reload, err)
	}
	if !reflect.DeepEqual(reload.Merged.Config.Checks, deliveredChecks("go-test", "web-unit")) || len(reload.Merged.OmittedChecks) != 0 {
		t.Fatalf("restored checks = %+v", reload.Merged.Config.Checks)
	}
}

func TestResolveStartsWhenALocalCheckRepositoryIsNotDelivered(t *testing.T) {
	local := testLocal(t)
	writeLocalFile(t, local, localChecksYAML)
	remote := dockerConfig()
	remote["repositories"] = map[string]any{}
	var gateways []string
	resolved, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(remote)), nil, &gateways), noSleepPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved.Merged.Config.Checks, deliveredChecks("go-test")) ||
		!reflect.DeepEqual(resolved.Merged.OmittedChecks, []OmittedCheck{{ID: "web-unit", RepositoryID: "repo-1"}}) {
		t.Fatalf("resolved = %+v", resolved.Merged)
	}
}

func TestResolveKeepsLocalChecksWhenItWithdrawsTheDirectAcknowledgement(t *testing.T) {
	local := testLocal(t)
	writeLocalFile(t, local, "acknowledge_direct: true\n"+localChecksYAML)
	var gateways []string
	if _, err := Resolve(context.Background(), local.StateDir, testRunnerID, fetchReturning(encode(t, deliveryBody(dockerConfig())), nil, &gateways), noSleepPolicy()); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLocal(local.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AcknowledgeDirect || !reflect.DeepEqual(loaded.Checks, localChecks()) {
		t.Fatalf("local after resolve = %+v", loaded)
	}
}
