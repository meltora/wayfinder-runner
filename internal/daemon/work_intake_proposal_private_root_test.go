package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/example/app-runner/internal/config"
)

func TestPreparePrivateExecutionRootIsOwnerOnly(t *testing.T) {
	generator := &providerWorkIntakeProposalGenerator{cfg: config.Config{StateDir: t.TempDir()}}
	root, err := generator.preparePrivateExecutionRoot()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(root) != filepath.Join(generator.cfg.StateDir, workIntakeProposalExecutionRootName) {
		t.Fatalf("execution root %s is outside the work intake parent", root)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("execution root mode = %v", info.Mode())
	}
}

func TestPreparePrivateExecutionRootRemovesTheDirectoryWhenItCannotBeRestricted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory read permission")
	}
	generator := &providerWorkIntakeProposalGenerator{cfg: config.Config{StateDir: t.TempDir()}}
	parent := filepath.Join(generator.cfg.StateDir, workIntakeProposalExecutionRootName)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if root, err := generator.preparePrivateExecutionRoot(); err == nil {
		t.Fatalf("execution root %s was prepared without a scoped parent handle", root)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed preparation left %d entries", len(entries))
	}
}
