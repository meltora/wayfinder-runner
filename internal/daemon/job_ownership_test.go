package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/supervisor"
)

var (
	testRootlessMapping = isolation.UserMapping{
		Mode: isolation.UserMappingRootless, ContainerUID: 10001, ContainerGID: 10001, HostUID: 110000, HostGID: 110000,
	}
	testIdentityMapping = isolation.UserMapping{
		Mode: isolation.UserMappingIdentity, ContainerUID: 1000, ContainerGID: 1000, HostUID: 1000, HostGID: 1000,
	}
)

type orderLog struct {
	mu      sync.Mutex
	entries []string
}

func (log *orderLog) add(entry string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.entries = append(log.entries, entry)
}

func (log *orderLog) String() string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return strings.Join(log.entries, " ")
}

type recordingOwnership struct {
	log        *orderLog
	mu         sync.Mutex
	handOffs   []isolation.OwnershipRequest
	reclaims   []isolation.OwnershipRequest
	handOffErr error
	reclaimErr error
	onHandOff  func(isolation.OwnershipRequest)
	onReclaim  func(isolation.OwnershipRequest)
}

func (o *recordingOwnership) HandOff(_ context.Context, request isolation.OwnershipRequest) error {
	o.mu.Lock()
	o.handOffs = append(o.handOffs, request)
	o.mu.Unlock()
	o.log.add("handoff:" + request.Purpose)
	if o.onHandOff != nil {
		o.onHandOff(request)
	}
	return o.handOffErr
}

func (o *recordingOwnership) Reclaim(_ context.Context, request isolation.OwnershipRequest) error {
	o.mu.Lock()
	o.reclaims = append(o.reclaims, request)
	o.mu.Unlock()
	o.log.add("reclaim:" + request.Purpose)
	if o.onReclaim != nil {
		o.onReclaim(request)
	}
	return o.reclaimErr
}

type ownershipTestProcess struct{ waitErr error }

func (ownershipTestProcess) PID() int            { return 4343 }
func (process ownershipTestProcess) Wait() error { return process.waitErr }
func (ownershipTestProcess) Interrupt() error    { return nil }
func (ownershipTestProcess) Kill() error         { return nil }
func (ownershipTestSupervisor) Name() string     { return "ownership-test" }
func (ownershipTestSupervisor) Available() bool  { return true }

type ownershipTestSupervisor struct {
	log       *orderLog
	spec      *supervisor.Spec
	startErr  error
	waitErr   error
	duringRun func()
}

func (value ownershipTestSupervisor) Start(_ context.Context, spec supervisor.Spec) (supervisor.Process, error) {
	value.log.add("start")
	*value.spec = spec
	if value.startErr != nil {
		return nil, value.startErr
	}
	if value.duringRun != nil {
		value.duringRun()
	}
	return ownershipTestProcess{waitErr: value.waitErr}, nil
}

type ownershipRun struct {
	*compositionRun
	log       *orderLog
	ownership *recordingOwnership
	sup       *ownershipTestSupervisor
	spec      *supervisor.Spec
	removeErr error
}

func installJobDocker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n  ps) exit 0 ;;\n  rm) exit 0 ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installProviderCredentials(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test","expiresAt":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func newOwnershipRun(t *testing.T, mapping isolation.UserMapping) *ownershipRun {
	t.Helper()
	run := &ownershipRun{compositionRun: newCompositionRun(t, false), log: &orderLog{}, spec: &supervisor.Spec{}}
	installJobDocker(t)
	installProviderCredentials(t)
	jr := run.jr
	stateDir, err := os.MkdirTemp("", "rdju-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	jr.d.cfg.StateDir = stateDir
	jr.d.runnerID = "runner-1"
	jr.d.cfg.Isolation = "docker"
	jr.d.cfg.JobNetwork = "none"
	jr.d.cfg.JobImage = "registry.example.test/job@sha256:" + strings.Repeat("a", 64)
	jr.d.verifiedProviderCLIs = map[providerCLIKey]string{{provider: "claude", image: jr.d.cfg.JobImage}: "2.1.299 (Claude Code)"}
	jr.d.userMapping = mapping
	if mapping.RequiresOwnershipHandOff() {
		run.ownership = &recordingOwnership{log: run.log}
		jr.d.ownership = run.ownership
	}
	jr.d.removeJobContainer = func(_ context.Context, runnerID, attemptID string) error {
		run.log.add("remove:" + attemptID)
		return run.removeErr
	}
	run.sup = &ownershipTestSupervisor{log: run.log, spec: run.spec}
	jr.d.supervisor = run.sup
	return run
}

func (run *ownershipRun) execute(t *testing.T) {
	t.Helper()
	run.jr.d.supervisor = *run.sup
	run.compositionRun.execute(t)
}

func (run *ownershipRun) expectFinished(t *testing.T, state, failureCode string) {
	t.Helper()
	finished := run.finished(t)
	if finished["state"] != state || str(finished, "failure_code") != failureCode {
		t.Fatalf("job_finished = %v, want state %s failure %q (calls %s)", finished, state, failureCode, run.log)
	}
}

func (run *ownershipRun) expectCalls(t *testing.T, want string) {
	t.Helper()
	if got := run.log.String(); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestProviderJobHandsOffBeforeStartAndReclaimsBeforeAfterExitWork(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	compositionsAtHandOff, compositionsAtReclaim := -1, -1
	run.ownership.onHandOff = func(isolation.OwnershipRequest) { compositionsAtHandOff = len(run.compositions()) }
	run.ownership.onReclaim = func(isolation.OwnershipRequest) { compositionsAtReclaim = len(run.compositions()) }
	run.execute(t)
	run.expectCalls(t, "handoff:job start remove:a1 reclaim:job")
	if compositionsAtHandOff != 1 || compositionsAtReclaim != 1 || len(run.compositions()) != 2 {
		t.Fatalf("composition reports at hand-off=%d reclaim=%d total=%d", compositionsAtHandOff, compositionsAtReclaim, len(run.compositions()))
	}
	handOff := run.ownership.handOffs[0]
	if handOff.RunnerID != "runner-1" || handOff.AttemptID != "a1" || handOff.Purpose != "job" ||
		!slices.Equal(handOff.Workspace, []string{run.jr.workDir}) || len(handOff.Private) != 2 {
		t.Fatalf("hand-off request = %+v", handOff)
	}
	if !strings.HasPrefix(handOff.Private[0], filepath.Join(run.jr.d.cfg.StateDir, "job-homes", "home-")) ||
		!strings.HasPrefix(handOff.Private[1], filepath.Join(run.jr.d.cfg.StateDir, "sockets", "job-")) {
		t.Fatalf("private roots = %v", handOff.Private)
	}
	if reclaim := run.ownership.reclaims[0]; !slices.Equal(reclaim.Workspace, handOff.Workspace) || !slices.Equal(reclaim.Private, handOff.Private) {
		t.Fatalf("reclaim request %+v differs from hand-off %+v", reclaim, handOff)
	}
	if users := argumentsAfter(run.spec.Args, "--user"); !slices.Equal(users, []string{"10001:10001"}) {
		t.Fatalf("--user values = %v", users)
	}
	for _, root := range append([]string{run.jr.workDir}, handOff.Private...) {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("%s survived the job: %v", root, err)
		}
	}
}

func TestProviderJobReclaimsOnEveryExitPath(t *testing.T) {
	for name, test := range map[string]struct {
		configure   func(*ownershipRun)
		calls       string
		state, code string
	}{
		"provider failure": {
			configure: func(run *ownershipRun) { run.sup.waitErr = errors.New("exit status 3") },
			calls:     "handoff:job start remove:a1 reclaim:job", state: "failed", code: string(v1.JobFailureAgentExit),
		},
		"start failure": {
			configure: func(run *ownershipRun) { run.sup.startErr = errors.New("docker run failed") },
			calls:     "handoff:job start remove:a1 reclaim:job", state: "failed", code: string(v1.JobFailureAgentStart),
		},
		"cancelled before start": {
			configure: func(run *ownershipRun) {
				run.ownership.onHandOff = func(isolation.OwnershipRequest) { run.jr.cancelled = true }
			},
			calls: "handoff:job remove:a1 reclaim:job", state: "cancelled", code: string(v1.JobFailureCancelled),
		},
		"cancelled while running": {
			configure: func(run *ownershipRun) {
				run.sup.waitErr = errors.New("killed")
				run.sup.duringRun = func() { run.jr.cancelled = true }
			},
			calls: "handoff:job start remove:a1 reclaim:job", state: "cancelled", code: string(v1.JobFailureCancelled),
		},
		"lease lost": {
			configure: func(run *ownershipRun) {
				run.sup.waitErr = errors.New("killed")
				run.sup.duringRun = func() { run.jr.leaseLost, run.jr.cancelled = true, true }
			},
			calls: "handoff:job start remove:a1 reclaim:job", state: "quarantined", code: string(v1.JobFailureLeaseLost),
		},
		"hand-off failure": {
			configure: func(run *ownershipRun) { run.ownership.handOffErr = errors.New("chown failed") },
			calls:     "handoff:job remove:a1 reclaim:job", state: "failed", code: string(v1.JobFailureWorkspacePreparation),
		},
		"reclaim failure": {
			configure: func(run *ownershipRun) { run.ownership.reclaimErr = errors.New("chown failed") },
			calls:     "handoff:job start remove:a1 reclaim:job", state: "quarantined", code: string(v1.JobFailureCleanup),
		},
		"container removal failure": {
			configure: func(run *ownershipRun) { run.removeErr = errors.New("daemon unavailable") },
			calls:     "handoff:job start remove:a1", state: "quarantined", code: string(v1.JobFailureCleanup),
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := newOwnershipRun(t, testRootlessMapping)
			test.configure(run)
			run.execute(t)
			run.expectCalls(t, test.calls)
			run.expectFinished(t, test.state, test.code)
		})
	}
}

func TestProviderJobSkipsAfterExitWorkWhenOutputCannotBeReowned(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	run.removeErr = errors.New("daemon unavailable")
	run.execute(t)
	if got := len(run.compositions()); got != 1 {
		t.Fatalf("composition reports = %d; after-exit work ran on a job-owned tree", got)
	}
}

func TestProviderJobOnAnIdentityMappingUsesTheMappedUserWithoutHandOff(t *testing.T) {
	run := newOwnershipRun(t, testIdentityMapping)
	run.execute(t)
	run.expectCalls(t, "start")
	if users := argumentsAfter(run.spec.Args, "--user"); !slices.Equal(users, []string{"1000:1000"}) {
		t.Fatalf("--user values = %v", users)
	}
}

func TestProviderJobRefusesAnUnresolvedMapping(t *testing.T) {
	run := newOwnershipRun(t, isolation.UserMapping{})
	run.execute(t)
	run.expectCalls(t, "")
	run.expectFinished(t, "failed", string(v1.JobFailureAgentStart))
}

func TestArtifactPublicationOfAnUnreadableJobFileFailsWithTheArtifactSourceError(t *testing.T) {
	jr, _ := testJobRunner(t)
	path := filepath.Join(jr.workDir, "private.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	_, err := jr.handlePublishArtifact(context.Background(), map[string]any{"name": "private", "path": "private.txt"})
	if err == nil || !strings.Contains(err.Error(), "artifact source") {
		t.Fatalf("err = %v", err)
	}
}

func TestOwnershipScopeRemovesTheContainerBeforeReclaimAndReleasesOnce(t *testing.T) {
	log := &orderLog{}
	ownership := &recordingOwnership{log: log}
	remove := func(_ context.Context, runnerID, attemptID string) error {
		log.add("remove:" + runnerID + "/" + attemptID)
		return nil
	}
	request := isolation.OwnershipRequest{RunnerID: "runner-1", AttemptID: "a1", Purpose: "job", Workspace: []string{"/w"}}
	scope, err := handOffOwnership(context.Background(), ownership, remove, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Release(); err != nil {
		t.Fatal(err)
	}
	if err := scope.Release(); err != nil {
		t.Fatal(err)
	}
	if got := log.String(); got != "handoff:job remove:runner-1/a1 reclaim:job" {
		t.Fatalf("calls = %q", got)
	}
	var nilScope *ownershipScope
	if err := nilScope.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipScopeNeverReclaimsWhileTheContainerMayRun(t *testing.T) {
	log := &orderLog{}
	ownership := &recordingOwnership{log: log}
	remove := func(context.Context, string, string) error {
		log.add("remove")
		return errors.New("daemon unavailable")
	}
	scope, err := handOffOwnership(context.Background(), ownership, remove, isolation.OwnershipRequest{Purpose: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Release(); err == nil {
		t.Fatal("release succeeded although the container was not removed")
	}
	if got := log.String(); got != "handoff:job remove" {
		t.Fatalf("calls = %q", got)
	}
}

func TestOwnershipScopeWithoutOwnershipOnlyRemovesTheContainer(t *testing.T) {
	removals := 0
	scope, err := handOffOwnership(context.Background(), nil, func(context.Context, string, string) error {
		removals++
		return nil
	}, isolation.OwnershipRequest{})
	if err != nil || scope.Release() != nil || removals != 1 {
		t.Fatalf("err=%v removals=%d", err, removals)
	}
}

func argumentsAfter(args []string, flag string) []string {
	var values []string
	for index, argument := range args {
		if argument == flag && index+1 < len(args) {
			values = append(values, args[index+1])
		}
	}
	return values
}
