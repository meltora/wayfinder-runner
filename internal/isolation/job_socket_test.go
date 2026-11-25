package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckJobSocketPathAcceptsTheLongestStateDirThatFits(t *testing.T) {
	t.Parallel()
	fits := "/srv/" + strings.Repeat("s", MaxUnixSocketPathBytes-len("/srv//sockets/job-4294967295/mcp.sock"))
	if err := CheckJobSocketPath(fits); err != nil {
		t.Fatalf("state_dir of %d bytes rejected: %v", len(fits), err)
	}
	if got := len(LongestJobSocketPath(fits)); got != MaxUnixSocketPathBytes {
		t.Fatalf("longest socket path = %d bytes, want %d", got, MaxUnixSocketPathBytes)
	}
	err := CheckJobSocketPath(fits + "x")
	want := "state_dir is too long for docker isolation: the job MCP socket path can reach 104 bytes, over the 103-byte unix socket limit; use a state_dir of at most 71 bytes"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestLongestJobSocketPathCoversEveryTemporaryDirectoryName(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sockets := filepath.Join(stateDir, "sockets")
	if err := os.Mkdir(sockets, 0o700); err != nil {
		t.Fatal(err)
	}
	for range 64 {
		dir, err := os.MkdirTemp(sockets, "job-")
		if err != nil {
			t.Fatal(err)
		}
		if actual := filepath.Join(dir, "mcp.sock"); len(actual) > len(LongestJobSocketPath(stateDir)) {
			t.Fatalf("%s is longer than the bound for its state_dir", actual)
		}
	}
}
