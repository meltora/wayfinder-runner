package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var wantClaudeRequiredCLIOptions = []string{
	"--allowedTools", "--bare", "--disallowedTools", "--input-format", "--json-schema",
	"--mcp-config", "--model", "--no-session-persistence", "--output-format", "--permission-mode",
	"--permission-prompts", "--plugin-dir", "--print", "--restricted", "--resume", "--setting-sources",
	"--settings", "--strict-mcp-config", "--tools", "--verbose",
}

func collectCLIOptions(args []string, into map[string]struct{}) {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "-p" {
			arg = "--print"
		}
		into[arg] = struct{}{}
	}
}

func sortedOptionSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for option := range set {
		out = append(out, option)
	}
	sort.Strings(out)
	return out
}

func TestClaudeRequiredCLIOptionsMatchEveryOptionTheRunnerPasses(t *testing.T) {
	collected := map[string]struct{}{}
	for _, readOnly := range []bool{false, true} {
		for _, hardened := range []bool{false, true} {
			for _, resume := range []bool{false, true} {
				for _, plugin := range []bool{false, true} {
					p := testParams(t)
					p.ReadOnly, p.HardenedRuntime = readOnly, hardened
					p.MCPBinary = "/usr/local/bin/our-runner"
					if resume {
						p.ResumeSessionID = "session-1"
					}
					if plugin {
						pluginDir := filepath.Join(p.PackDir, ".claude-plugin")
						if err := os.MkdirAll(pluginDir, 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"test"}`), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					spec, err := NewClaude().BuildSpec(p)
					if err != nil {
						t.Fatal(err)
					}
					collectCLIOptions(spec.Args, collected)
				}
			}
		}
	}
	promptOnly, err := NewClaude().BuildPromptOnlySpec(promptOnlyParams(t))
	if err != nil {
		t.Fatal(err)
	}
	collectCLIOptions(promptOnly.Args, collected)
	got := sortedOptionSet(collected)
	required := NewClaude().RequiredCLIOptions()
	if !reflect.DeepEqual(got, required) {
		t.Fatalf("options passed = %q\nRequiredCLIOptions = %q", got, required)
	}
	if !reflect.DeepEqual(required, wantClaudeRequiredCLIOptions) {
		t.Fatalf("RequiredCLIOptions = %q, want %q", required, wantClaudeRequiredCLIOptions)
	}
}

func TestRequiredCLIOptionsAreSortedUniqueAndACopy(t *testing.T) {
	var contract CLIOptionContract = NewClaude()
	first := contract.RequiredCLIOptions()
	if !sort.StringsAreSorted(first) {
		t.Fatalf("options are not sorted: %q", first)
	}
	for index := 1; index < len(first); index++ {
		if first[index] == first[index-1] {
			t.Fatalf("duplicate option %q", first[index])
		}
	}
	first[0] = "--mutated"
	if second := contract.RequiredCLIOptions(); second[0] != "--allowedTools" {
		t.Fatalf("contract shares its backing array: %q", second)
	}
}

func TestOnlyClaudeDeclaresACLIOptionContract(t *testing.T) {
	for _, name := range []string{"codex", "opencode"} {
		adapter, err := For(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := adapter.(CLIOptionContract); ok {
			t.Fatalf("%s declares an option contract in this slice", name)
		}
	}
	adapter, _ := For("claude")
	if _, ok := adapter.(CLIOptionContract); !ok {
		t.Fatal("claude does not declare an option contract")
	}
}

func readHelpFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMissingCLIOptionsAgainstRealHelpTexts(t *testing.T) {
	required := NewClaude().RequiredCLIOptions()
	for name, want := range map[string][]string{
		"claude-help-2.1.221.txt": {"--permission-prompts", "--restricted"},
		"claude-help-2.1.299.txt": nil,
		"claude-help-2.1.326.txt": nil,
	} {
		t.Run(name, func(t *testing.T) {
			got := MissingCLIOptions(readHelpFixture(t, name), required)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("missing = %q, want %q", got, want)
			}
		})
	}
}

func TestMissingCLIOptionsWithAnEmptyHelpReportsEveryOption(t *testing.T) {
	required := NewClaude().RequiredCLIOptions()
	if got := MissingCLIOptions("", required); !reflect.DeepEqual(got, required) {
		t.Fatalf("missing = %q", got)
	}
}

func TestMissingCLIOptionsIgnoresOptionsNamedOnlyInDescriptions(t *testing.T) {
	help := strings.Join([]string{
		"Usage: claude [options]",
		"",
		"Options:",
		"  --bare                                Minimal mode. Combine with --restricted",
		"                                        --restricted to remove tools.",
		"      --restricted is not declared here",
		"   --restricted",
		"\t--restricted",
		"  --allowedTools, --allowed-tools <tools...>",
		"      Comma or space-separated list of tool names to allow",
		"  -p, --print                           Print response and exit",
		"  --permission-prompts <target>         Where prompts go (choices: \"host\", \"none\")\r",
		"  --tmux=classic                        Not a real line shape",
		"  --Bad_Name                            Not a valid option name",
		"",
		"Commands:",
		"  doctor                                Check --restricted health",
	}, "\n")
	declared := DeclaredCLIOptions(help)
	want := []string{"--allowed-tools", "--allowedTools", "--bare", "--permission-prompts", "--print", "--tmux"}
	if got := sortedOptionSet(declared); !reflect.DeepEqual(got, want) {
		t.Fatalf("declared = %q, want %q", got, want)
	}
	got := MissingCLIOptions(help, []string{"--restricted", "--print", "--allowed-tools", "--restricted"})
	if !reflect.DeepEqual(got, []string{"--restricted"}) {
		t.Fatalf("missing = %q", got)
	}
}
