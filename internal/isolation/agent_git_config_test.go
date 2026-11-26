package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAgentGitConfigSetsIdentityAndDisablesUpstreamSetup(t *testing.T) {
	home := t.TempDir()
	if err := WriteAgentGitConfig(home); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gitconfig")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name = " + AgentGitName, "email = " + AgentGitEmail, "autoSetupMerge = false"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("agent Git config misses %q:\n%s", want, content)
		}
	}
}

func TestWriteAgentGitConfigIgnoresClaudeSandboxPlaceholders(t *testing.T) {
	home := t.TempDir()
	if err := WriteAgentGitConfig(home); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "[core]\n\texcludesFile = "+ContainerHome+"/"+agentGitExcludesName+"\n") {
		t.Fatalf("agent Git config does not point at the runner excludes file:\n%s", config)
	}
	path := filepath.Join(home, agentGitExcludesName)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("excludes mode = %v", info.Mode())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	want := []string{
		"/.bash_profile", "/.bashrc", "/.claude/", "/.gitconfig", "/.gitmodules", "/.idea", "/.mcp.json",
		"/.profile", "/.ripgreprc", "/.vscode", "/.zprofile", "/.zshrc",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("excludes = %q, want %q", got, want)
	}
}

func TestWriteAgentGitConfigReplacesAPlantedSymlink(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".gitconfig")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAgentGitConfig(home); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "keep\n" {
		t.Fatalf("outside file changed: %q %v", content, err)
	}
}
