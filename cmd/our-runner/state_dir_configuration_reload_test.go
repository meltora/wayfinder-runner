package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/remoteconfig"
)

func TestStateDirReloaderFollowsTheRunningDigest(t *testing.T) {
	h := newConfigureHarness(t)
	key := newConfigurePackKey(t, "pack-key-1")
	delivered := configureDelivery(t, h.runnerID, "docker", key)
	h.respond(http.StatusOK, delivered)
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	cfg, reloader, err := loadStateDirConfigurationWithReloader(context.Background(), h.stateDir, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	running := strings.Repeat("c", 64)
	if cfg.RunnerConfigurationDigest != running || reloader.RunningDigest() != running {
		t.Fatalf("digests = %q %q", cfg.RunnerConfigurationDigest, reloader.RunningDigest())
	}
	h.respond(http.StatusNotModified, nil)
	reload, err := reloader.Check(context.Background())
	if err != nil || reload.Outcome != remoteconfig.ReloadUnchanged {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	requests := h.server.ConfigurationRequests()
	if last := requests[len(requests)-1]; last.IfNoneMatch != `"`+running+`"` || last.ClientName != h.runnerID {
		t.Fatalf("request = %+v", last)
	}
	hot := bytes.Replace(delivered, []byte(running), []byte(strings.Repeat("e", 64)), 1)
	hot = bytes.Replace(hot, []byte(`"labels":{}`), []byte(`"labels":{"pool":"hot"}`), 1)
	h.respond(http.StatusOK, hot)
	reload, err = reloader.Check(context.Background())
	if err != nil || reload.Outcome != remoteconfig.ReloadApply || reload.Merged.Config.Labels["pool"] != "hot" {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	if len(reload.Merged.Config.AgentPackPublicKeys) != 0 {
		t.Fatalf("a remote pack key was trusted: %+v", reload.Merged.Config.AgentPackPublicKeys)
	}
}

func TestStateDirConfigurationWithACachedStartStillReloads(t *testing.T) {
	h := newConfigureHarness(t)
	delivered := configureDelivery(t, h.runnerID, "docker")
	h.respond(http.StatusOK, delivered)
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	h.respond(http.StatusServiceUnavailable, []byte(`{"code":"unavailable"}`))
	_, reloader, err := loadStateDirConfigurationWithReloader(context.Background(), h.stateDir, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if reloader.RunningDigest() != strings.Repeat("c", 64) {
		t.Fatalf("running digest = %q", reloader.RunningDigest())
	}
	if _, err := reloader.Check(context.Background()); !remoteconfig.IsTransient(err) {
		t.Fatalf("err = %v", err)
	}
}
