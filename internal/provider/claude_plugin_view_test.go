package provider

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func writePackFile(t *testing.T, packDir, name, content string, perm os.FileMode) {
	t.Helper()
	path := filepath.Join(packDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func argumentAfter(args []string, flag string) string {
	for index, argument := range args {
		if argument == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func viewFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			relative, _ := filepath.Rel(root, path)
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func elevatedPack(t *testing.T, packDir string) {
	t.Helper()
	writePackFile(t, packDir, ".claude-plugin/plugin.json", `{
  "name": "shortstack-platform",
  "version": "1.2.0",
  "description": "Platform pack",
  "author": {"name": "Platform"},
  "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/usr/bin/touch /tmp/inline-hook"}]}]},
  "mcpServers": {"pack": {"command": "/usr/bin/pack-mcp"}},
  "lspServers": "./.lsp.json",
  "experimental": {"monitors": "./monitors/monitors.json"},
  "skills": ["./extra-skills/"],
  "commands": "./more-commands/",
  "agents": ["./agents/reviewer.md"],
  "outputStyles": "./output-styles/",
  "defaultEnabled": false,
  "userConfig": {"token": {"type": "string", "title": "t", "description": "d"}}
}`, 0o600)
	writePackFile(t, packDir, "hooks/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/usr/bin/touch /tmp/pack-hook"}]}]}}`, 0o600)
	writePackFile(t, packDir, ".mcp.json", `{"mcpServers":{"pack":{"command":"/usr/bin/pack-mcp"}}}`, 0o600)
	writePackFile(t, packDir, ".lsp.json", `{"go":{"command":"gopls","extensionToLanguage":{".go":"go"}}}`, 0o600)
	writePackFile(t, packDir, "monitors/monitors.json", `[{"name":"m","command":"tail -F x","description":"d"}]`, 0o600)
	writePackFile(t, packDir, "bin/pack-tool", "#!/bin/sh\n", 0o700)
	writePackFile(t, packDir, "settings.json", `{"agent":"reviewer"}`, 0o600)
	writePackFile(t, packDir, "extra-skills/x/SKILL.md", "---\ndescription: extra\n---\nbody\n", 0o600)
	writePackFile(t, packDir, ".claude/hooks/format.json", `{"hooks":{}}`, 0o600)
	writePackFile(t, packDir, "skills/plan/SKILL.md", "---\nname: plan\ndescription: Plans work and never mentions hook config keys\n---\nUse the script.\n", 0o600)
	writePackFile(t, packDir, "skills/plan/scripts/run.sh", "#!/bin/sh\necho plan\n", 0o700)
	writePackFile(t, packDir, "skills/sneaky/SKILL.md", "---\nname: sneaky\nhooks:\n  PreToolUse:\n    - hooks:\n        - type: command\n          command: /usr/bin/touch /tmp/skill-hook\n---\nbody\n", 0o600)
	writePackFile(t, packDir, "skills/sneaky/notes.md", "plain notes\n", 0o600)
	writePackFile(t, packDir, "commands/ok.md", "---\ndescription: ok\n---\nRun it.\n", 0o600)
	writePackFile(t, packDir, "commands/bad.md", "---\ndescription: bad\nHooks: {Stop: []}\n---\nRun it.\n", 0o600)
	writePackFile(t, packDir, "agents/reviewer.md", "---\nname: reviewer\ndescription: reviews\n---\nReview.\n", 0o600)
	writePackFile(t, packDir, "WORKFLOW.md", "pack instructions\n", 0o600)
}

func TestClaudePluginViewWithholdsPackHooksMCPAndExecutableComponents(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = false
	elevatedPack(t, p.PackDir)
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(p.ConfigDir, "claude-plugin")
	if got := argumentAfter(spec.Args, "--plugin-dir"); got != view {
		t.Fatalf("--plugin-dir = %q, want runner-owned view %q", got, view)
	}
	if got := argumentAfter(spec.Args, "--add-dir"); got != p.PackDir {
		t.Fatalf("--add-dir = %q, want %q", got, p.PackDir)
	}
	want := []string{
		".claude-plugin/plugin.json",
		"agents/reviewer.md",
		"commands/ok.md",
		"skills/plan/SKILL.md",
		"skills/plan/scripts/run.sh",
	}
	if got := viewFiles(t, view); !reflect.DeepEqual(got, want) {
		t.Fatalf("plugin view files = %q, want %q", got, want)
	}
	manifest, err := os.ReadFile(filepath.Join(view, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	wantManifest := map[string]any{
		"name": "shortstack-platform", "version": "1.2.0", "description": "Platform pack",
		"author": map[string]any{"name": "Platform"},
	}
	if !reflect.DeepEqual(decoded, wantManifest) {
		t.Fatalf("plugin view manifest = %v, want %v", decoded, wantManifest)
	}
	info, err := os.Stat(filepath.Join(view, "skills", "plan", "scripts", "run.sh"))
	if err != nil || info.Mode().Perm()&0o100 == 0 || info.Mode().Perm()&0o022 != 0 {
		t.Fatalf("copied script mode = %v, err = %v", info, err)
	}
	for _, argument := range spec.Args {
		if strings.HasPrefix(argument, p.PackDir) && argument != p.PackDir {
			t.Fatalf("pack-internal path handed to Claude Code: %q", argument)
		}
	}
	if !strings.Contains(strings.Join(spec.Args, " "), "--strict-mcp-config") {
		t.Fatalf("Claude spec lacks --strict-mcp-config: %v", spec.Args)
	}
}

func TestClaudePluginViewIsRebuiltWhenTheSpecIsBuiltAgain(t *testing.T) {
	p := testParams(t)
	elevatedPack(t, p.PackDir)
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.PackDir, "commands", "ok.md")); err != nil {
		t.Fatal(err)
	}
	p.DockerIsolation = true
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	view := argumentAfter(spec.Args, "--plugin-dir")
	for _, name := range viewFiles(t, view) {
		if name == "commands/ok.md" {
			t.Fatalf("stale plugin view file survived a rebuild: %q", name)
		}
	}
}

func TestClaudePluginViewAbsentWithoutPluginManifest(t *testing.T) {
	p := testParams(t)
	writePackFile(t, p.PackDir, "skills/plan/SKILL.md", "---\nname: plan\n---\n", 0o600)
	writePackFile(t, p.PackDir, "hooks/hooks.json", `{"hooks":{}}`, 0o600)
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := argumentAfter(spec.Args, "--plugin-dir"); got != "" {
		t.Fatalf("--plugin-dir passed without a plugin manifest: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(p.ConfigDir, "claude-plugin")); !os.IsNotExist(err) {
		t.Fatalf("plugin view written without a plugin manifest: %v", err)
	}
}

func TestClaudePluginViewRefusesInvalidManifest(t *testing.T) {
	for name, manifest := range map[string]string{
		"not json":       `{`,
		"array":          `[]`,
		"missing name":   `{"version":"1"}`,
		"empty name":     `{"name":""}`,
		"numeric name":   `{"name":1}`,
		"numeric author": `{"name":"x","author":1}`,
		"array version":  `{"name":"x","version":[]}`,
		"duplicate name": `{"name":"x","name":"y"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := testParams(t)
			writePackFile(t, p.PackDir, ".claude-plugin/plugin.json", manifest, 0o600)
			if _, err := NewClaude().BuildSpec(p); err == nil {
				t.Fatalf("manifest %s accepted", manifest)
			}
		})
	}
}

func TestClaudePluginViewSkipsSymbolicLinks(t *testing.T) {
	p := testParams(t)
	writePackFile(t, p.PackDir, ".claude-plugin/plugin.json", `{"name":"x"}`, 0o600)
	writePackFile(t, p.PackDir, "skills/plan/SKILL.md", "---\nname: plan\n---\n", 0o600)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.PackDir, "skills", "plan", "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(p.PackDir, "hooks"), filepath.Join(p.PackDir, "agents")); err != nil {
		t.Fatal(err)
	}
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".claude-plugin/plugin.json", "skills/plan/SKILL.md"}
	if got := viewFiles(t, argumentAfter(spec.Args, "--plugin-dir")); !reflect.DeepEqual(got, want) {
		t.Fatalf("plugin view files = %q, want %q", got, want)
	}
}

func TestMarkdownFrontmatterDeclaresHooks(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    bool
	}{
		"no frontmatter":          {"# Title\nhooks: are mentioned here\n", false},
		"clean":                   {"---\nname: plan\ndescription: plan\n---\nbody\n", false},
		"empty frontmatter":       {"---\n---\nbody\n", false},
		"hooks key":               {"---\nhooks:\n  Stop: []\n---\n", true},
		"capitalised key":         {"---\nHooks: {}\n---\n", true},
		"quoted key":              {"---\n\"hooks\": {}\n---\n", true},
		"escaped key":             {"---\n\"\\x68ooks\": {}\n---\n", true},
		"spaced key":              {"---\nhooks : {}\n---\n", true},
		"flow mapping":            {"---\n{name: x, hooks: {}}\n---\n", true},
		"merge key":               {"---\nbase: &b {hooks: {}}\n<<: *b\n---\n", true},
		"unterminated":            {"---\nname: x\n", true},
		"invalid yaml":            {"---\nname: [\n---\n", true},
		"scalar frontmatter":      {"---\njust text\n---\n", true},
		"byte order mark":         {"\ufeff---\nhooks: {}\n---\n", true},
		"leading blank line":      {"\n---\nhooks: {}\n---\n", true},
		"opening with trailing":   {"--- # c\nname: x\n---\n", true},
		"crlf hooks":              {"---\r\nhooks: {}\r\n---\r\n", true},
		"crlf clean":              {"---\r\nname: x\r\n---\r\n", false},
		"nested hooks value only": {"---\ndescription: \"hooks\"\nmetadata:\n  hooks: x\n---\n", false},
		"duplicate keys":          {"---\nname: a\nname: b\n---\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := markdownFrontmatterDeclaresHooks([]byte(tc.content)); got != tc.want {
				t.Fatalf("markdownFrontmatterDeclaresHooks(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}
