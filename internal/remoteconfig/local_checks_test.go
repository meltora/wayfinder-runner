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
			DependencyDirectories: []string{"node_modules"}},
	}
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
    dependency_directories: [node_modules]
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
	for name, remote := range map[string]map[string]any{"docker": dockerConfig(), "direct": directConfig()} {
		local := testLocal(t)
		local.AcknowledgeDirect = true
		local.Checks = localChecks()
		if name == "direct" {
			check := local.Checks["web-unit"]
			check.RepositoryID = ""
			local.Checks["web-unit"] = check
		}
		want := config.Normalize(config.Config{Checks: local.Checks}).Checks
		merged, err := Merge(local, parsed(t, deliveryBody(remote)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(merged.Config.Checks, want) {
			t.Fatalf("%s: checks = %+v", name, merged.Config.Checks)
		}
		merged.Config.Checks["go-test"].Args[0] = "changed"
		if local.Checks["go-test"].Args[0] != "test" {
			t.Fatalf("%s: merged checks alias the local file", name)
		}
	}
	merged, err := Merge(testLocal(t), parsed(t, deliveryBody(dockerConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Config.Checks) != 0 {
		t.Fatalf("checks without a local definition = %+v", merged.Config.Checks)
	}
}

func TestMergeValidatesLocalChecksLikeAConfigFile(t *testing.T) {
	for name, check := range map[string]config.CheckConfig{
		"empty command":          {Command: ""},
		"long timeout":           {Command: "go", TimeoutSeconds: 1801},
		"negative timeout":       {Command: "go", TimeoutSeconds: -1},
		"unregistered repo":      {Command: "go", RepositoryID: "repo-unknown"},
		"escaping dependency":    {Command: "go", DependencyDirectories: []string{"../outside"}},
		"too many args":          {Command: "go", Args: make([]string, 129)},
		"oversized command name": {Command: strings.Repeat("g", 513)},
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
		"unknown check key": "checks:\n  go-test:\n    command: go\n    install: true\n",
		"env on a check":    "checks:\n  go-test:\n    command: go\n    env: {GOFLAGS: -x}\n",
		"list of checks":    "checks:\n  - command: go\n",
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
	want := config.Normalize(config.Config{Checks: localChecks()}).Checks
	if !reflect.DeepEqual(started.Merged.Config.Checks, want) {
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
	if !reflect.DeepEqual(reload.Merged.Config.Checks, want) || reload.Merged.Config.Labels["pool"] != "us-linux" {
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

func TestReloaderRefusesADocumentThatDropsTheRepositoryOfALocalCheck(t *testing.T) {
	local := testLocal(t)
	local.Checks = localChecks()
	next := dockerConfig()
	next["repositories"] = map[string]any{"repo-2": map[string]any{"url": "https://github.com/acme/web.git"}}
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("e", 64)))}}}
	reloader := runningReloader(t, local, deliveryBody(dockerConfig()), script.fetch)
	reload, err := reloader.Check(context.Background())
	if err == nil || reload.Outcome != ReloadUnchanged || !strings.Contains(err.Error(), "check web-unit references unregistered repository repo-1") {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	if reloader.RunningDigest() != strings.Repeat("d", 64) {
		t.Fatal("the running document changed")
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
