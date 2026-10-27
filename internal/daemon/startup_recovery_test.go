package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func makeWorkIntakeExecutions(t *testing.T, stateDir string) (string, string) {
	t.Helper()
	parent := filepath.Join(stateDir, workIntakeProposalExecutionRootName)
	execution := filepath.Join(parent, "execution-a1")
	locked := filepath.Join(execution, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if err := os.Mkdir(filepath.Join(parent, "not-an-execution"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(parent, "execution-link")); err != nil {
		t.Fatal(err)
	}
	return execution, locked
}

func TestStartupRecoveryReclaimsStaleWorkIntakeExecutionsBeforeRemovingThem(t *testing.T) {
	stateDir := t.TempDir()
	execution, _ := makeWorkIntakeExecutions(t, stateDir)
	if err := os.Remove(filepath.Join(stateDir, workIntakeProposalExecutionRootName, "execution-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "jobs", "job-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	err := recoverStartupJobState(stateDir, true, func(_ context.Context, roots []string) error {
		for _, root := range roots {
			if _, err := os.Lstat(root); err != nil {
				t.Fatalf("root %s was removed before reclaim: %v", root, err)
			}
		}
		calls = append(calls, slices.Clone(roots))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !slices.Equal(calls[0], []string{execution}) || !slices.Equal(calls[1], []string{filepath.Join(stateDir, "jobs", "job-1")}) {
		t.Fatalf("reclaim calls = %v", calls)
	}
	for _, gone := range []string{execution, filepath.Join(stateDir, "jobs", "job-1")} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived recovery: %v", gone, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(stateDir, workIntakeProposalExecutionRootName, "not-an-execution")); err != nil {
		t.Fatalf("unrelated directory removed: %v", err)
	}
}

func TestStartupRecoveryReportsAReclaimFailureWithTheCleanupFailure(t *testing.T) {
	stateDir := t.TempDir()
	execution, locked := makeWorkIntakeExecutions(t, stateDir)
	reclaimFailure := errors.New("helper refused")
	var offered []string
	err := recoverStartupJobState(stateDir, true, func(_ context.Context, roots []string) error {
		offered = append(offered, roots...)
		return reclaimFailure
	})
	if !errors.Is(err, reclaimFailure) || !strings.Contains(err.Error(), "re-own stale work intake proposal executions") || !strings.Contains(err.Error(), "cleanup stale work intake proposal executions") {
		t.Fatalf("recovery err = %v", err)
	}
	if !slices.Equal(offered, []string{execution}) {
		t.Fatalf("offered %v, want only the real execution directory", offered)
	}
	if _, err := os.Lstat(locked); err != nil {
		t.Fatalf("unreclaimed execution content was removed: %v", err)
	}
}

func TestStartupRecoveryKeepsJobStateWhenALeaseWasRecovered(t *testing.T) {
	stateDir := t.TempDir()
	job := filepath.Join(stateDir, "jobs", "job-1")
	if err := os.MkdirAll(job, 0o700); err != nil {
		t.Fatal(err)
	}
	var reclaimed []string
	if err := recoverStartupJobState(stateDir, false, func(_ context.Context, roots []string) error {
		reclaimed = append(reclaimed, roots...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("reclaimed %v with a recovered lease", reclaimed)
	}
	if _, err := os.Lstat(job); err != nil {
		t.Fatalf("job state removed with a recovered lease: %v", err)
	}
}

func TestStartupRecoveryWithoutOwnershipRemovesRunnerOwnedExecutions(t *testing.T) {
	stateDir := t.TempDir()
	execution := filepath.Join(stateDir, workIntakeProposalExecutionRootName, "execution-b2")
	if err := os.MkdirAll(filepath.Join(execution, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recoverStartupJobState(stateDir, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(execution); !os.IsNotExist(err) {
		t.Fatalf("execution survived: %v", err)
	}
}
