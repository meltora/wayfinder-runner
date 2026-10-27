package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/doctor"
	"github.com/example/app-runner/internal/remoteconfig"
)

func restrictedConfigureDelivery(t *testing.T, runnerID string) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(configureDelivery(t, runnerID, "docker"), &body); err != nil {
		t.Fatal(err)
	}
	document := body["config"].(map[string]any)
	document["job_network"] = "restricted"
	document["job_network_name"] = "our-runner-jobs-dev"
	document["egress_policy_id"] = "local-dev-egress-not-enforced"
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func jobNetworkLines(output string) []string {
	var lines []string
	for _, line := range strings.SplitAfter(output, "\n") {
		if strings.HasPrefix(line, "Job network:") {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestConfigureSummaryNamesTheJobNetwork(t *testing.T) {
	for name, test := range map[string]struct {
		remote remoteconfig.Remote
		want   []string
	}{
		"none": {remoteconfig.Remote{Isolation: "docker", JobImage: configureJobImage, JobNetwork: "none"},
			[]string{"Job network: none (jobs have no network access)\n"}},
		"absent": {remoteconfig.Remote{Isolation: "docker", JobImage: configureJobImage},
			[]string{"Job network: none (jobs have no network access)\n"}},
		"restricted": {remoteconfig.Remote{Isolation: "docker", JobImage: configureJobImage, JobNetwork: "restricted", JobNetworkName: "our-runner-jobs-dev", EgressPolicyID: "local-dev-egress-not-enforced"},
			[]string{"Job network: restricted, Docker network our-runner-jobs-dev, egress policy local-dev-egress-not-enforced (checked against the network labels at start)\n"}},
		"direct": {remoteconfig.Remote{Isolation: "direct", AllowUnsafeDirect: true}, nil},
	} {
		var output bytes.Buffer
		test.remote.Provider = "claude"
		printConfigureSummary(&output, remoteconfig.Delivery{RunnerID: "runner-1", ProfileRevision: 1, Remote: test.remote})
		got := jobNetworkLines(output.String())
		if strings.Join(got, "") != strings.Join(test.want, "") {
			t.Fatalf("%s: lines = %q, want %q", name, got, test.want)
		}
	}
}

func TestConfigureStoresAndReloadsTheRestrictedJobNetwork(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, restrictedConfigureDelivery(t, h.runnerID))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "Job network: restricted, Docker network our-runner-jobs-dev, egress policy local-dev-egress-not-enforced") {
		t.Fatalf("output = %s", h.output.String())
	}
	h.respond(http.StatusServiceUnavailable, []byte(`{"code":"unavailable"}`))
	warnings := &bytes.Buffer{}
	cfg, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings)
	if err != nil || cfg.JobNetwork != "restricted" || cfg.JobNetworkName != "our-runner-jobs-dev" || cfg.EgressPolicyID != "local-dev-egress-not-enforced" {
		t.Fatalf("cached cfg = %+v err = %v", cfg, err)
	}
	var captured doctor.Options
	output := &bytes.Buffer{}
	err = cmdDoctorWithDependencies([]string{"--state-dir", h.stateDir}, doctorCommandDependencies{
		load: func(string) (config.Config, error) {
			t.Fatal("state-dir mode loaded a config file")
			return config.Config{}, nil
		},
		loadStateDir: func(stateDir string) (config.Config, error) {
			return loadStateDirConfiguration(context.Background(), stateDir, output)
		},
		run: func(_ context.Context, opts doctor.Options) doctor.Report {
			captured = opts
			return doctor.Report{}
		},
		output: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured.JobNetwork != "restricted" || captured.JobNetworkName != "our-runner-jobs-dev" || captured.EgressPolicyID != "local-dev-egress-not-enforced" {
		t.Fatalf("doctor options = %+v", captured)
	}
}
