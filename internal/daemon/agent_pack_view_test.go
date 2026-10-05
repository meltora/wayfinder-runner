package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/example/app-runner/internal/agentpack"
)

func mountTestPackView(t *testing.T, files map[string][]byte) (string, map[string]string) {
	t.Helper()
	var archive bytes.Buffer
	if _, err := agentpack.WriteCanonicalArchive(&archive, agentpack.ArchiveContent{
		PackKey: "platform", Provider: "claude-code", Version: "1.0.0", Files: files,
	}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "pack")
	writableCleanupJobView(t, destination)
	mounted, err := agentpack.MountAuthenticatedArchiveWithPolicy(&archive, destination, agentpack.MountPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	return destination, mounted.Manifest.Files
}

func defaultTestPackFiles() map[string][]byte {
	return map[string][]byte{
		".claude-plugin/plugin.json": []byte(`{"name":"shortstack-platform"}`),
		"skills/plan/SKILL.md":       []byte("# plan"),
		"WORKFLOW.md":                []byte("# workflow"),
	}
}

func withWritable(t *testing.T, path string, mode os.FileMode, change func()) {
	t.Helper()
	original, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	change()
	if err := os.Chmod(path, original.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAgentPackViewAcceptsTheUntouchedMount(t *testing.T) {
	root, files := mountTestPackView(t, defaultTestPackFiles())
	if err := verifyAgentPackView(root, files); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAgentPackViewDetectsEveryTamperClass(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"content rewritten": func(t *testing.T, root string) {
			path := filepath.Join(root, "WORKFLOW.md")
			withWritable(t, path, 0o644, func() { _ = os.WriteFile(path, []byte("# evil!!!!"), 0o644) })
		},
		"file chmod": func(t *testing.T, root string) { _ = os.Chmod(filepath.Join(root, "WORKFLOW.md"), 0o644) },
		"directory chmod": func(t *testing.T, root string) {
			_ = os.Chmod(filepath.Join(root, "skills"), 0o755)
		},
		"root chmod": func(t *testing.T, root string) { _ = os.Chmod(root, 0o755) },
		"extra file": func(t *testing.T, root string) {
			withWritable(t, root, 0o755, func() { _ = os.WriteFile(filepath.Join(root, "extra.md"), []byte("x"), 0o444) })
		},
		"extra directory": func(t *testing.T, root string) {
			withWritable(t, root, 0o755, func() { _ = os.Mkdir(filepath.Join(root, "extra"), 0o555) })
		},
		"file removed": func(t *testing.T, root string) {
			withWritable(t, root, 0o755, func() { _ = os.Remove(filepath.Join(root, "WORKFLOW.md")) })
		},
		"file replaced by symlink": func(t *testing.T, root string) {
			withWritable(t, root, 0o755, func() {
				_ = os.Remove(filepath.Join(root, "WORKFLOW.md"))
				_ = os.Symlink("/etc/hosts", filepath.Join(root, "WORKFLOW.md"))
			})
		},
		"view removed": func(t *testing.T, root string) {
			_ = os.Chmod(root, 0o755)
			_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err == nil && info.IsDir() {
					_ = os.Chmod(path, 0o755)
				}
				return nil
			})
			_ = os.RemoveAll(root)
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			root, files := mountTestPackView(t, defaultTestPackFiles())
			tamper(t, root)
			if err := verifyAgentPackView(root, files); err == nil {
				t.Fatalf("%s was not detected", name)
			}
		})
	}
}

func TestVerifyAgentPackViewRequiresAnAuthenticatedManifest(t *testing.T) {
	root, _ := mountTestPackView(t, defaultTestPackFiles())
	if err := verifyAgentPackView(root, nil); err == nil {
		t.Fatal("view verified without an authenticated manifest")
	}
}
