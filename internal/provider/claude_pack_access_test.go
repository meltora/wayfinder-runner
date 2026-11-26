package provider

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestClaudeAgentPackIsReadableButNeverASandboxWriteRoot(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		p := testParams(t)
		p.ReadOnly = readOnly
		p.DockerIsolation = true
		spec, err := NewClaude().BuildSpec(p)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(spec.Args, "--add-dir") {
			t.Fatalf("readOnly=%v args = %q, the read-only pack must not be a sandbox write root", readOnly, spec.Args)
		}
		settings := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-settings.json"))
		allow, _ := settings["permissions"].(map[string]any)["allow"].([]any)
		want := []any{"Read(/" + filepath.Clean(p.PackDir) + "/**)"}
		if !reflect.DeepEqual(allow, want) {
			t.Fatalf("readOnly=%v permissions.allow = %v, want %v", readOnly, allow, want)
		}
	}
}

func TestClaudeWithoutAgentPackAllowsNoExtraReads(t *testing.T) {
	p := testParams(t)
	p.PackDir = ""
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatal(err)
	}
	settings := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-settings.json"))
	if _, present := settings["permissions"].(map[string]any)["allow"]; present {
		t.Fatalf("permissions = %v", settings["permissions"])
	}
}
