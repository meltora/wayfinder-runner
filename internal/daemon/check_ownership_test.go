package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/isolation"
)

type checkOwnershipHarness struct {
	jr        *jobRunner
	log       *orderLog
	ownership *recordingOwnership
	root      string
	removeErr error
}

func newCheckOwnershipHarness(t *testing.T) *checkOwnershipHarness {
	t.Helper()
	jr, _ := testJobRunner(t)
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  *\"exit 7\"*) exit 7 ;;\n  *\"sleep\"*) sleep 5 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	harness := &checkOwnershipHarness{jr: jr, log: &orderLog{}}
	harness.ownership = &recordingOwnership{log: harness.log}
	jr.d.runnerID = "runner-1"
	jr.d.cfg.Isolation = "docker"
	jr.d.cfg.JobNetwork = "none"
	jr.d.cfg.JobImage = "registry.example.test/job@sha256:" + strings.Repeat("a", 64)
	jr.d.userMapping = testRootlessMapping
	jr.d.ownership = harness.ownership
	jr.d.removeJobContainer = func(_ context.Context, _, attemptID string) error {
		harness.log.add("remove")
		return harness.removeErr
	}
	harness.root = checkWorkspaceRoot(jr.d.cfg.StateDir, jr.workDir)
	if err := os.MkdirAll(filepath.Join(harness.root, "primary"), 0o700); err != nil {
		t.Fatal(err)
	}
	return harness
}

func (h *checkOwnershipHarness) run(ctx context.Context, purpose, script string) error {
	_, _, err := h.jr.runWorkspaceCommand(ctx, purpose, h.root, filepath.Join(h.root, "primary"), "/bin/sh", []string{"-c", script}, false, 1024)
	return err
}

func checkHomes(t *testing.T, stateDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, "check-homes"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestCheckCommandHandsOffItsWorkspaceAndHomeAndReclaimsAfterRemovingTheContainer(t *testing.T) {
	h := newCheckOwnershipHarness(t)
	if err := h.run(context.Background(), "check-lint", "true"); err != nil {
		t.Fatal(err)
	}
	if got := h.log.String(); got != "handoff:check remove reclaim:check" {
		t.Fatalf("calls = %q", got)
	}
	request := h.ownership.handOffs[0]
	wantAttempt := h.jr.job.AttemptID + "-task-" + shortDigest("check-lint")
	if request.RunnerID != "runner-1" || request.AttemptID != wantAttempt || !slices.Equal(request.Workspace, []string{h.root}) ||
		len(request.Private) != 1 || !strings.HasPrefix(request.Private[0], filepath.Join(h.jr.d.cfg.StateDir, "check-homes", "home-")) {
		t.Fatalf("hand-off request = %+v", request)
	}
	if homes := checkHomes(t, h.jr.d.cfg.StateDir); len(homes) != 0 {
		t.Fatalf("check homes left: %v", homes)
	}
}

func TestCheckCommandReclaimsWhenTheCommandFailsOrIsCancelled(t *testing.T) {
	h := newCheckOwnershipHarness(t)
	if err := h.run(context.Background(), "check-fail", "exit 7"); err == nil {
		t.Fatal("failing check reported success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.run(ctx, "check-cancel", "sleep 5"); err == nil {
		t.Fatal("cancelled check reported success")
	}
	if got := h.log.String(); got != "handoff:check remove reclaim:check handoff:check remove reclaim:check" {
		t.Fatalf("calls = %q", got)
	}
}

func TestSequentialChecksEachHandOffAndReclaim(t *testing.T) {
	h := newCheckOwnershipHarness(t)
	for _, purpose := range []string{"check-a", "check-b"} {
		if err := h.run(context.Background(), purpose, "true"); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.log.String(); got != "handoff:check remove reclaim:check handoff:check remove reclaim:check" {
		t.Fatalf("calls = %q", got)
	}
}

func TestCheckCommandHandOffFailureNeverStartsTheContainer(t *testing.T) {
	h := newCheckOwnershipHarness(t)
	h.ownership.handOffErr = errors.New("chown failed")
	if err := h.run(context.Background(), "check-lint", "true"); err == nil {
		t.Fatal("hand-off failure was ignored")
	}
	if got := h.log.String(); got != "handoff:check remove reclaim:check" {
		t.Fatalf("calls = %q", got)
	}
	if homes := checkHomes(t, h.jr.d.cfg.StateDir); len(homes) != 0 {
		t.Fatalf("check homes left: %v", homes)
	}
}

func TestCheckCommandKeepsTheHomeForRecoveryWhenOutputCannotBeReowned(t *testing.T) {
	for name, configure := range map[string]func(*checkOwnershipHarness){
		"reclaim failure":           func(h *checkOwnershipHarness) { h.ownership.reclaimErr = errors.New("chown failed") },
		"container removal failure": func(h *checkOwnershipHarness) { h.removeErr = errors.New("daemon unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newCheckOwnershipHarness(t)
			configure(h)
			if err := h.run(context.Background(), "check-lint", "true"); err == nil {
				t.Fatal("cleanup failure was ignored")
			}
			if homes := checkHomes(t, h.jr.d.cfg.StateDir); len(homes) != 1 {
				t.Fatalf("check homes = %v", homes)
			}
		})
	}
}

func TestCheckCommandUsesTheResolvedUser(t *testing.T) {
	h := newCheckOwnershipHarness(t)
	policy := h.jr.commandPolicy("/w", "/w", "/h", "a1-task-x", false)
	if policy.User != "10001:10001" {
		t.Fatalf("check policy user = %q", policy.User)
	}
	h.jr.d.userMapping = isolation.UserMapping{}
	h.jr.d.ownership = nil
	if err := h.run(context.Background(), "check-lint", "true"); err == nil {
		t.Fatal("unresolved mapping accepted")
	}
}
