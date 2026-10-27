package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func makeRecoveryTree(t *testing.T, stateDir string) []string {
	t.Helper()
	reclaimable := []string{
		filepath.Join(stateDir, "jobs", "job-1"),
		filepath.Join(stateDir, "job-homes", "home-1"),
		filepath.Join(stateDir, "sockets", "job-1"),
		filepath.Join(stateDir, "check-homes", "home-1"),
		filepath.Join(stateDir, trustedResultsDirectory, "job-1.checks"),
	}
	for _, dir := range append(append([]string(nil), reclaimable...),
		filepath.Join(stateDir, trustedResultsDirectory, "job-1", "primary"),
		filepath.Join(stateDir, "tmux", "job-1"),
	) {
		if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested", "file"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(stateDir, "jobs", "job-link")); err != nil {
		t.Fatal(err)
	}
	return reclaimable
}

func TestRecoveryReclaimsLeftoverJobTreesBeforeRemovingThem(t *testing.T) {
	stateDir := t.TempDir()
	want := makeRecoveryTree(t, stateDir)
	var reclaimed []string
	err := cleanupOrphanedJobState(context.Background(), stateDir, func(_ context.Context, roots []string) error {
		for _, root := range roots {
			if _, err := os.Stat(root); err != nil {
				t.Fatalf("root %s was removed before reclaim: %v", root, err)
			}
		}
		reclaimed = append(reclaimed, roots...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(reclaimed)
	slices.Sort(want)
	if !slices.Equal(reclaimed, want) {
		t.Fatalf("reclaimed %v, want %v", reclaimed, want)
	}
	for _, parent := range []string{"jobs", "job-homes", "sockets", "check-homes", trustedResultsDirectory, "tmux"} {
		entries, err := os.ReadDir(filepath.Join(stateDir, parent))
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s entries = %v err=%v", parent, entries, err)
		}
	}
}

func TestRecoveryReclaimsInBatchesOfEightThroughTheOwnershipHelper(t *testing.T) {
	d := &Daemon{runnerID: "runner-1"}
	if d.recoveryReclaimer() != nil {
		t.Fatal("recovery reclaim offered without ownership")
	}
	ownership := &recordingOwnership{log: &orderLog{}}
	d.ownership = ownership
	roots := make([]string, 17)
	for index := range roots {
		roots[index] = fmt.Sprintf("/state/jobs/job-%d", index)
	}
	if err := d.recoveryReclaimer()(context.Background(), roots); err != nil {
		t.Fatal(err)
	}
	if got := ownership.log.String(); got != "reclaim:recover-1 reclaim:recover-2 reclaim:recover-3" {
		t.Fatalf("calls = %q", got)
	}
	for _, request := range ownership.reclaims {
		if request.RunnerID != "runner-1" || request.AttemptID != "recovery" || len(request.Private) > 8 {
			t.Fatalf("request = %+v", request)
		}
	}
}

func TestRecoveryNamesThePathWhenJobOutputCannotBeReowned(t *testing.T) {
	for name, reclaim := range map[string]func(context.Context, []string) error{
		"no ownership helper": nil,
		"reclaim failed":      func(context.Context, []string) error { return errors.New("docker unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			job := filepath.Join(stateDir, "jobs", "job-1")
			if err := os.MkdirAll(filepath.Join(job, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			jobs := filepath.Dir(job)
			if err := os.Chmod(jobs, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(jobs, 0o700) })
			err := cleanupOrphanedJobState(context.Background(), stateDir, reclaim)
			if err == nil || !strings.Contains(err.Error(), job) || !strings.Contains(err.Error(), "the runner could not re-own job output") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
