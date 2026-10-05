package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const savedEnvironmentValue = "/home/runner/.claude-private-account"

func configureDeliveryForProvider(t *testing.T, runnerID, provider string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(configureDelivery(t, runnerID, "docker"), &document); err != nil {
		t.Fatal(err)
	}
	document["config"].(map[string]any)["provider"] = provider
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func executableFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProviderDisplayNamesFallBackToTheID(t *testing.T) {
	for id, want := range map[string]string{"codex": "Codex", "claude": "Claude Code", "opencode": "OpenCode", "hermes": "hermes"} {
		if got := providerDisplayName(id); got != want {
			t.Fatalf("%s = %q, want %q", id, got, want)
		}
	}
}

func TestConfigureNamesTheProviderInSummaryAndPrompts(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     []string
		absent   []string
	}{
		{"claude", []string{"Provider: Claude Code", "CLAUDE_CONFIG_DIR for Claude Code (optional; Enter leaves it unset): ", "Path to the Claude Code binary (optional; press Enter to find it on PATH): "}, []string{"Provider: claude"}},
		{"codex", []string{"Provider: Codex", "CODEX_HOME for Codex (optional; Enter leaves it unset): ", "Path to the Codex binary (optional; press Enter to find it on PATH): "}, []string{"Provider: codex"}},
		{"opencode", []string{"Provider: OpenCode", "Path to the OpenCode binary (optional; press Enter to find it on PATH): "}, []string{"Provider: opencode", " for OpenCode (optional; Enter leaves"}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			h := newConfigureHarness(t)
			h.respond(http.StatusOK, configureDeliveryForProvider(t, h.runnerID, tc.provider))
			if err := h.run("\n\n", true); err != nil {
				t.Fatal(err)
			}
			out := h.output.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("output missing %q:\n%s", want, out)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Fatalf("output has %q:\n%s", absent, out)
				}
			}
		})
	}
}

func TestConfigureBinaryPromptKeepsClearsAndReplacesTheSavedPath(t *testing.T) {
	saved := executableFile(t, "claude")
	replacement := executableFile(t, "claude-next")
	for _, tc := range []struct {
		name, answer, want string
		fails              bool
	}{
		{name: "keep", answer: "", want: saved},
		{name: "clear", answer: "-", want: ""},
		{name: "replace", answer: replacement, want: replacement},
		{name: "invalid", answer: "relative/claude", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfigureHarness(t)
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
			if err := h.run("", false, "--non-interactive", "--provider-binary", saved); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(h.stateDir, "runner.local.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			h.output.Reset()
			err = h.run("\n"+tc.answer+"\n", true)
			if !strings.Contains(h.output.String(), "Path to the Claude Code binary [current: "+saved+"] (Enter keeps it, - finds it on PATH): ") {
				t.Fatalf("prompt:\n%s", h.output.String())
			}
			if strings.Contains(h.output.String(), "not an executable file") {
				t.Fatalf("valid binary reported missing:\n%s", h.output.String())
			}
			if tc.fails {
				if err == nil || !strings.Contains(err.Error(), "Claude Code binary path must be absolute") {
					t.Fatalf("err = %v", err)
				}
				after, readErr := os.ReadFile(filepath.Join(h.stateDir, "runner.local.yaml"))
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("local settings changed after a failed configure: %v", readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			binary, ok := h.local(t).ProviderBinaries["claude"]
			if binary != tc.want || ok != (tc.want != "") {
				t.Fatalf("binary = %q present=%v, want %q", binary, ok, tc.want)
			}
		})
	}
}

func TestConfigureBinaryPromptReportsAMissingSavedBinaryBeforeAsking(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	missing := filepath.Join(t.TempDir(), "removed-claude")
	if err := h.run("", false, "--non-interactive", "--provider-binary", missing); err != nil {
		t.Fatal(err)
	}
	h.output.Reset()
	if err := h.run("\n\n", true); err != nil {
		t.Fatal(err)
	}
	out := h.output.String()
	notice := "The saved Claude Code binary " + missing + " is not an executable file."
	prompt := "Path to the Claude Code binary [current: " + missing + "]"
	if !strings.Contains(out, notice) || strings.Index(out, notice) > strings.Index(out, prompt) {
		t.Fatalf("output:\n%s", out)
	}
	directory := t.TempDir()
	if err := h.run("", false, "--non-interactive", "--provider-binary", directory); err != nil {
		t.Fatal(err)
	}
	h.output.Reset()
	if err := h.run("\n\n", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "The saved Claude Code binary "+directory+" is not an executable file.") {
		t.Fatalf("directory accepted as binary:\n%s", h.output.String())
	}
}

func TestConfigureProviderEnvironmentPromptKeepsClearsAndReplacesWithoutPrintingTheValue(t *testing.T) {
	for _, tc := range []struct {
		name, answer, want string
	}{
		{name: "keep", answer: "", want: savedEnvironmentValue},
		{name: "clear", answer: "-", want: ""},
		{name: "replace", answer: "/home/runner/.claude-replacement", want: "/home/runner/.claude-replacement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfigureHarness(t)
			h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
			if err := h.run("", false, "--non-interactive", "--provider-env", "CLAUDE_CONFIG_DIR="+savedEnvironmentValue); err != nil {
				t.Fatal(err)
			}
			if err := h.run(tc.answer+"\n\n", true); err != nil {
				t.Fatal(err)
			}
			out := h.output.String()
			if !strings.Contains(out, "CLAUDE_CONFIG_DIR for Claude Code (currently set; Enter keeps it, - clears it): ") {
				t.Fatalf("prompt:\n%s", out)
			}
			for _, secret := range []string{savedEnvironmentValue, "/home/runner/.claude-replacement"} {
				if strings.Contains(out, secret) {
					t.Fatalf("output printed a provider environment value:\n%s", out)
				}
			}
			variables, ok := h.local(t).ProviderEnvironment["claude"]
			if variables["CLAUDE_CONFIG_DIR"] != tc.want || ok != (tc.want != "") {
				t.Fatalf("environment = %v present=%v", variables, ok)
			}
		})
	}
}

func TestConfigureProviderEnvironmentClearWhenUnsetLeavesItUnset(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("-\n\n", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.local(t).ProviderEnvironment["claude"]; ok {
		t.Fatal("a single - was saved as the environment value")
	}
}

func TestConfigureFlagsOverrideSavedLocalValuesWithoutPrompting(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	saved := executableFile(t, "claude")
	if err := h.run("", false, "--non-interactive", "--provider-binary", saved, "--provider-env", "CLAUDE_CONFIG_DIR="+savedEnvironmentValue); err != nil {
		t.Fatal(err)
	}
	replacement := executableFile(t, "claude-next")
	h.output.Reset()
	if err := h.run("", true, "--provider-binary", replacement, "--provider-env", "CLAUDE_CONFIG_DIR=/home/runner/.claude-flag"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output.String(), "Path to the") || strings.Contains(h.output.String(), "currently set") {
		t.Fatalf("prompted despite flags:\n%s", h.output.String())
	}
	local := h.local(t)
	if local.ProviderBinaries["claude"] != replacement || local.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/home/runner/.claude-flag" {
		t.Fatalf("local = %+v", local)
	}
}
