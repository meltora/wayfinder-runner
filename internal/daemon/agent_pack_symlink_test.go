package daemon

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestVerifyAgentPackViewRefusesASymlinkToIdenticalOutsideContent(t *testing.T) {
	cases := map[string]func(t *testing.T, root, outside string){
		"file": func(t *testing.T, root, outside string) {
			writeFixtureFile(t, outside, "WORKFLOW.md", "# workflow", 0o444)
			withWritable(t, root, 0o755, func() {
				if err := os.Remove(filepath.Join(root, "WORKFLOW.md")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "WORKFLOW.md"), filepath.Join(root, "WORKFLOW.md")); err != nil {
					t.Fatal(err)
				}
			})
		},
		"directory": func(t *testing.T, root, outside string) {
			writeFixtureFile(t, outside, "skills/plan/SKILL.md", "# plan", 0o444)
			for _, dir := range []string{"skills/plan", "skills"} {
				if err := os.Chmod(filepath.Join(outside, dir), 0o555); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				_ = os.Chmod(filepath.Join(outside, "skills"), 0o755)
				_ = os.Chmod(filepath.Join(outside, "skills/plan"), 0o755)
			})
			withWritable(t, root, 0o755, func() {
				for _, dir := range []string{"skills", "skills/plan"} {
					if err := os.Chmod(filepath.Join(root, dir), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(filepath.Join(root, "skills"), filepath.Join(t.TempDir(), "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "skills"), filepath.Join(root, "skills")); err != nil {
					t.Fatal(err)
				}
			})
		},
	}
	for name, replace := range cases {
		t.Run(name, func(t *testing.T) {
			root, files := mountTestPackView(t, defaultTestPackFiles())
			replace(t, root, t.TempDir())
			if err := verifyAgentPackView(root, files); err == nil {
				t.Fatalf("a %s symlink to identical outside content was accepted", name)
			}
		})
	}
}

func TestCompositionNeverDigestsASymlinkedPlatformConfigurationFile(t *testing.T) {
	primary := t.TempDir()
	input := compositionInputFor(t, primary)
	outside := t.TempDir()
	writeFixtureFile(t, outside, "claude-hooks.json", `{"outside":true}`, 0o600)
	if err := os.Symlink(filepath.Join(outside, "claude-hooks.json"), filepath.Join(input.ConfigDir, "claude-hooks.json")); err != nil {
		t.Fatal(err)
	}
	outsideDigest := fileDigest(t, filepath.Join(outside, "claude-hooks.json"))
	body, err := buildAgentPackComposition(input)
	if err != nil {
		return
	}
	safety := layerByName(t, body, v1.AgentPackCompositionLayerPlatformSafety)
	for _, source := range safety.Sources {
		if source.Path == "claude-hooks.json" || source.DigestSHA256 == outsideDigest {
			t.Fatalf("symlinked platform configuration was digested: %+v", source)
		}
	}
	if len(safety.Sources) != 2 {
		t.Fatalf("platform safety sources = %+v", safety.Sources)
	}
}
