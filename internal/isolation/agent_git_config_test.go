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
