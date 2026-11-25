package isolation

import (
	"fmt"
	"path/filepath"
)

const MaxUnixSocketPathBytes = 103

func LongestJobSocketPath(stateDir string) string {
	return filepath.Join(stateDir, "sockets", "job-4294967295", "mcp.sock")
}

func CheckJobSocketPath(stateDir string) error {
	longest := LongestJobSocketPath(stateDir)
	if len(longest) <= MaxUnixSocketPathBytes {
		return nil
	}
	return fmt.Errorf("state_dir is too long for docker isolation: the job MCP socket path can reach %d bytes, over the %d-byte unix socket limit; use a state_dir of at most %d bytes",
		len(longest), MaxUnixSocketPathBytes, len(filepath.Clean(stateDir))-(len(longest)-MaxUnixSocketPathBytes))
}
