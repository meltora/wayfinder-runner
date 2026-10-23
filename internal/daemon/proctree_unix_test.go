//go:build !windows

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunWorkspaceCommandKillsDescendantProcesses(t *testing.T) {
	jr, _ := runCheckFixture(t)
	dir := jr.repositories[0].WorkDir
	sentinel := filepath.Join(dir, ".descendant-sentinel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _, err := jr.runWorkspaceCommand(ctx, "proctree-descendant", dir, dir, "/bin/sh",
		[]string{"-c", "sleep 5; touch \"" + sentinel + "\""}, false, 1<<20)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("cancelled command returned success")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("descendant kept the command alive past cancellation: %v", elapsed)
	}
	if remaining := 5*time.Second + 500*time.Millisecond - time.Since(start); remaining > 0 {
		time.Sleep(remaining)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatal("descendant survived cancellation and wrote its delayed sentinel")
	}
}

func TestConfigureProcessTreeOwnsFreshGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "true")
	configureProcessTree(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("command was not placed in its own process group")
	}
	if cmd.Cancel == nil {
		t.Fatal("cancellation hook was not installed")
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestKillOwnedProcessGroupNeverSignalsForeignGroup(t *testing.T) {
	if err := killOwnedProcessGroup(nil); err != nil {
		t.Fatalf("nil process: %v", err)
	}
	exited := exec.Command("/bin/sh", "-c", "true")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	if err := killOwnedProcessGroup(exited.Process); err != nil {
		t.Fatalf("exited process: %v", err)
	}
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := killOwnedProcessGroup(cmd.Process); err != nil {
		t.Fatalf("guarded kill: %v", err)
	}
	_ = cmd.Wait()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("guarded kill took too long: %v", elapsed)
	}
}
