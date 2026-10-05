package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgentSpecsCarryProviderEnvironmentSeparately(t *testing.T) {
	for _, adapter := range []Adapter{NewCodex(), NewClaude(), NewOpenCode()} {
		t.Run(adapter.Name(), func(t *testing.T) {
			p := testParams(t)
			p.HardenedRuntime = false
			p.Environment = []string{"ACCOUNT_DIR=/accounts/xogito"}
			spec, err := adapter.BuildSpec(p)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spec.ProviderEnvironment, []string{"ACCOUNT_DIR=/accounts/xogito"}) {
				t.Fatalf("provider environment = %v", spec.ProviderEnvironment)
			}
			p.Environment[0] = "ACCOUNT_DIR=/mutated"
			if spec.ProviderEnvironment[0] != "ACCOUNT_DIR=/accounts/xogito" {
				t.Fatal("spec aliases caller environment")
			}
			for _, entry := range append(spec.Env, ContainerEnvironment(spec.Env)...) {
				if strings.HasPrefix(entry, "ACCOUNT_DIR=") {
					t.Fatalf("provider environment mixed into runner environment: %v", spec.Env)
				}
			}
		})
	}
}

func TestAgentSpecsWithoutProviderEnvironmentAreUnchanged(t *testing.T) {
	for _, adapter := range []Adapter{NewCodex(), NewClaude(), NewOpenCode()} {
		spec, err := adapter.BuildSpec(testParams(t))
		if err != nil {
			t.Fatal(err)
		}
		if spec.ProviderEnvironment != nil {
			t.Fatalf("%s provider environment = %v", adapter.Name(), spec.ProviderEnvironment)
		}
	}
}

func TestClaudeSettingsDenyConfiguredConfigDirectory(t *testing.T) {
	p := testParams(t)
	p.Environment = []string{"CLAUDE_CONFIG_DIR=/Users/operator/.claude-xogito"}
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(filepath.Join(p.ConfigDir, "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(settings)
	for _, want := range []string{
		`"Read(//Users/operator/.claude-xogito/**)"`,
		`"Edit(//Users/operator/.claude-xogito/**)"`,
		`"/Users/operator/.claude-xogito"`,
		`"/Users/operator/.claude-xogito/.credentials.json"`,
		`"/Users/operator/.claude-xogito/.claude.json"`,
		`"~/.claude/.credentials.json"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings %s missing %s", text, want)
		}
	}
	for _, unwanted := range []string{`"//Users/operator/.claude-xogito"`, `"//Users/operator/.claude-xogito/.credentials.json"`, `"//Users/operator/.claude-xogito/.claude.json"`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("sandbox settings %s use permission-rule prefix %s", text, unwanted)
		}
	}
}
