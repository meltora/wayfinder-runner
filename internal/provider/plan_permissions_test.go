package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var wantPlanAllowedTools = []string{"mcp__platform__*", "Read", "Grep", "Glob", "Skill"}

var wantPlanDeniedTools = []string{
	"Edit", "Write", "NotebookEdit", "MultiEdit", "WebFetch", "WebSearch",
	"Bash(find *-delete*)", "Bash(find *-exec*)", "Bash(find *-ok*)", "Bash(find *-fprint*)", "Bash(find *-fls*)",
	"Bash(git *--output*)", "Bash(git *--ext-diff*)", "Bash(git *--textconv*)",
	"Bash(rg *--pre*)", "Bash(rg *--hostname-bin*)",
}

func flagValues(args []string, flag string) ([]string, int) {
	count := 0
	var values []string
	for i, arg := range args {
		if arg != flag {
			continue
		}
		count++
		for _, value := range args[i+1:] {
			if strings.HasPrefix(value, "--") {
				break
			}
			values = append(values, value)
		}
	}
	return values, count
}

type claudeSettingsDocument struct {
	Permissions struct {
		Deny []string `json:"deny"`
	} `json:"permissions"`
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func readClaudeSettings(t *testing.T, configDir string) claudeSettingsDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(configDir, "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings claudeSettingsDocument
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestClaudePlanSpecUsesExplicitReadOnlyAllowlist(t *testing.T) {
	for _, hardened := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "docker"}[hardened], func(t *testing.T) {
			p := testParams(t)
			p.ReadOnly = true
			p.HardenedRuntime = hardened
			p.MCPBinary = "/usr/local/bin/our-runner"
			spec, err := NewClaude().BuildSpec(p)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(spec.Args, " ")
			allowed, _ := flagValues(spec.Args, "--allowedTools")
			for _, rule := range allowed {
				if strings.HasPrefix(rule, "Bash") {
					t.Fatalf("plan args allow Bash by rule %q", rule)
				}
			}
			for _, forbidden := range []string{"--permission-mode plan", "acceptEdits", "bypassPermissions", "--dangerously-skip-permissions", "--setting-sources user", "--bare", "--safe-mode"} {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("plan args contain %q: %q", forbidden, joined)
				}
			}
			for flag, want := range map[string][]string{
				"--permission-mode":    {"dontAsk"},
				"--permission-prompts": {"none"},
				"--tools":              {"Read,Grep,Glob,Bash,Skill"},
				"--allowedTools":       wantPlanAllowedTools,
				"--disallowedTools":    wantPlanDeniedTools,
				"--setting-sources":    {""},
				"--restricted":         nil,
			} {
				got, count := flagValues(spec.Args, flag)
				if count != 1 || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s count=%d values=%q want %q", flag, count, got, want)
				}
			}
			settings := readClaudeSettings(t, p.ConfigDir)
			wantDeny := append([]string{
				"Read(~/.claude/**)", "Edit(~/.claude/**)", "Read(~/.claude.json)", "Edit(~/.claude.json)",
			}, wantPlanDeniedTools...)
			if !reflect.DeepEqual(settings.Permissions.Deny, wantDeny) {
				t.Fatalf("deny=%q want %q", settings.Permissions.Deny, wantDeny)
			}
		})
	}
}

func TestClaudePlanSpecKeepsConfigDirectoryDenyRules(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = true
	p.HardenedRuntime = false
	p.Environment = []string{"CLAUDE_CONFIG_DIR=/home/runner/.claude-config"}
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatal(err)
	}
	wantDeny := append([]string{
		"Read(~/.claude/**)", "Edit(~/.claude/**)", "Read(~/.claude.json)", "Edit(~/.claude.json)",
		"Read(//home/runner/.claude-config/**)", "Edit(//home/runner/.claude-config/**)",
	}, wantPlanDeniedTools...)
	if got := readClaudeSettings(t, p.ConfigDir).Permissions.Deny; !reflect.DeepEqual(got, wantDeny) {
		t.Fatalf("deny=%q want %q", got, wantDeny)
	}
}

func TestClaudePlanSpecRegistersRunnerCommandGuard(t *testing.T) {
	for _, hardened := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "docker"}[hardened], func(t *testing.T) {
			p := testParams(t)
			p.ReadOnly = true
			p.HardenedRuntime = hardened
			p.MCPBinary = "/usr/local/bin/our-runner"
			if _, err := NewClaude().BuildSpec(p); err != nil {
				t.Fatal(err)
			}
			guardPath := filepath.Join(p.ConfigDir, "plan-command-guard")
			info, err := os.Stat(guardPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o500 {
				t.Fatalf("guard mode=%v", info.Mode().Perm())
			}
			script, err := os.ReadFile(guardPath)
			if err != nil {
				t.Fatal(err)
			}
			wantScript := "#!/bin/sh\nexec '/usr/local/bin/our-runner' plan-command-guard --root " +
				shellQuote(p.WorkDir) + " --root " + shellQuote(p.PackDir) + "\n"
			if string(script) != wantScript {
				t.Fatalf("guard script=%q want %q", script, wantScript)
			}
			settings := readClaudeSettings(t, p.ConfigDir)
			var guards, managed int
			for _, entry := range settings.Hooks["PreToolUse"] {
				for _, hook := range entry.Hooks {
					switch {
					case entry.Matcher == "Bash" && hook.Command == shellQuote(guardPath):
						if hook.Type != "command" || hook.Timeout != 30 || len(entry.Hooks) != 1 {
							t.Fatalf("guard hook=%+v", entry)
						}
						guards++
					case entry.Matcher == "" && strings.HasSuffix(hook.Command, "managed-hook-pre-tool'"):
						managed++
					default:
						t.Fatalf("unexpected PreToolUse hook %+v", entry)
					}
				}
			}
			wantManaged := 0
			if hardened {
				wantManaged = 1
				if len(settings.Hooks) != len(claudeManagedHookDefinitions) {
					t.Fatalf("managed hook events=%d", len(settings.Hooks))
				}
			} else if len(settings.Hooks) != 1 {
				t.Fatalf("direct plan hooks=%v", settings.Hooks)
			}
			if guards != 1 || managed != wantManaged {
				t.Fatalf("guards=%d managed=%d", guards, managed)
			}
		})
	}
}

func TestClaudePlanSpecGuardWithoutPackUsesWorkspaceRootOnly(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = true
	p.PackDir = ""
	p.MCPBinary = "/usr/local/bin/our-runner"
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(p.ConfigDir, "plan-command-guard"))
	if err != nil {
		t.Fatal(err)
	}
	if string(script) != "#!/bin/sh\nexec '/usr/local/bin/our-runner' plan-command-guard --root "+shellQuote(p.WorkDir)+"\n" {
		t.Fatalf("guard script=%q", script)
	}
}

func TestClaudeBuildSpecHasNoCommandGuard(t *testing.T) {
	for _, hardened := range []bool{false, true} {
		p := testParams(t)
		p.ReadOnly = false
		p.HardenedRuntime = hardened
		if _, err := NewClaude().BuildSpec(p); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(p.ConfigDir, "plan-command-guard")); !os.IsNotExist(err) {
			t.Fatalf("build job wrote a plan command guard: %v", err)
		}
		for _, entry := range readClaudeSettings(t, p.ConfigDir).Hooks["PreToolUse"] {
			if entry.Matcher != "" {
				t.Fatalf("build job PreToolUse hook=%+v", entry)
			}
		}
	}
}

func TestClaudePlanSpecRejectsUnsafeGuardPaths(t *testing.T) {
	mutations := map[string]func(*StartParams){
		"workdir newline":    func(p *StartParams) { p.WorkDir += "\nx" },
		"workdir NUL":        func(p *StartParams) { p.WorkDir += "\x00" },
		"workdir CR":         func(p *StartParams) { p.WorkDir += "\r" },
		"workdir relative":   func(p *StartParams) { p.WorkDir = "relative" },
		"workdir empty":      func(p *StartParams) { p.WorkDir = "" },
		"pack newline":       func(p *StartParams) { p.PackDir += "\n" },
		"pack relative":      func(p *StartParams) { p.PackDir = "pack" },
		"binary newline":     func(p *StartParams) { p.MCPBinary = "/usr/local/bin/our-runner\n" },
		"binary relative":    func(p *StartParams) { p.MCPBinary = "our-runner" },
		"config dir CR":      func(p *StartParams) { p.ConfigDir += "\r" },
		"config dir newline": func(p *StartParams) { p.ConfigDir += "\n" },
	}
	for name, mutate := range mutations {
		for _, hardened := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				p := testParams(t)
				p.ReadOnly = true
				p.HardenedRuntime = hardened
				p.MCPBinary = "/usr/local/bin/our-runner"
				mutate(&p)
				if _, err := NewClaude().BuildSpec(p); err == nil {
					t.Fatalf("unsafe guard path accepted (hardened=%v)", hardened)
				}
			})
		}
	}
}
