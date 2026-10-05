package daemon_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func cacheStartupArchive(t *testing.T, stateDir string, body []byte) (string, string) {
	t.Helper()
	cache := agentpack.ArchiveCache{Root: filepath.Join(stateDir, "agent-pack-cache", "archives"), MaxArchiveBytes: agentpack.DefaultMaxArchiveBytes}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	path, err := cache.Acquire(context.Background(), digest, func(_ context.Context, writer io.Writer) error {
		_, err := writer.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest, path
}

func TestDaemonStartEvictsARevokedCachedAgentPackBeforeConnecting(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	revokedDigest, revokedPath := cacheStartupArchive(t, stateDir, []byte("revoked archive"))
	_, keptPath := cacheStartupArchive(t, stateDir, []byte("kept archive"))

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.RevokedAgentPackDigests = []string{revokedDigest}
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(revokedPath); err != nil {
		t.Fatalf("daemon.New touched the cache before the start path: %v", err)
	}
	if cp.HelloCount() != 0 {
		t.Fatalf("hello count = %d before start", cp.HelloCount())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}()

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })
	for _, path := range []string{revokedPath, strings.TrimSuffix(revokedPath, ".tgz") + ".lock"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains when the runner connects: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Lstat(keptPath); err != nil {
		t.Fatalf("unrevoked archive removed: %v", err)
	}
}
