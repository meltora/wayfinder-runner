package mcpbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func statSocket(path string) (os.FileInfo, error) { return os.Lstat(path) }

func validatePrivateSocketParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect MCP socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("MCP socket parent must be a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("MCP socket parent must not be accessible by group or others")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("MCP socket parent must be owned by the runner user")
	}
	return nil
}
