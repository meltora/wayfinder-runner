package daemon

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/supervisor"
)

type planHandOffProcess struct{ provider func() }

func (planHandOffProcess) PID() int         { return 4545 }
func (planHandOffProcess) Interrupt() error { return nil }
func (planHandOffProcess) Kill() error      { return nil }
func (process planHandOffProcess) Wait() error {
	process.provider()
	return nil
}

type planHandOffSupervisor struct {
	log      *orderLog
	onStart  func()
	provider func()
}

func (planHandOffSupervisor) Name() string    { return "plan-handoff-test" }
func (planHandOffSupervisor) Available() bool { return true }
func (value planHandOffSupervisor) Start(context.Context, supervisor.Spec) (supervisor.Process, error) {
	value.log.add("start")
	value.onStart()
	return planHandOffProcess{provider: value.provider}, nil
}

type planHandOffRun struct {
	*ownershipRun
	t          *testing.T
	ids        []string
	logs       *lockedWriter
	mu         sync.Mutex
	prepared   []planCheckoutDigests
	atStart    []planCheckoutDigests
	beforeHand func()
	provider   func()
	publishErr error
	published  bool
}

func newPlanHandOffRun(t *testing.T, mapping isolation.UserMapping, repositories int) *planHandOffRun {
	t.Helper()
	run := &planHandOffRun{ownershipRun: newOwnershipRun(t, mapping), t: t, ids: []string{"repo-1"}}
	for index := 2; index <= repositories; index++ {
		id := "repo-" + strconv.Itoa(index)
		addPlanHandOffRepository(t, run.jr, id)
		run.ids = append(run.ids, id)
	}
	run.logs = &lockedWriter{buffer: &bytes.Buffer{}}
	run.jr.logger = slog.New(slog.NewTextHandler(run.logs, nil))
	run.provider = func() { run.publish(publishPlanTool, "handoff-plan", "") }
	if run.ownership != nil {
		run.ownership.onHandOff = func(isolation.OwnershipRequest) {
			run.capturePrepared()
			if run.beforeHand != nil {
				run.beforeHand()
			}
		}
	}
	return run
}

func newDirectPlanHandOffRun(t *testing.T) *planHandOffRun {
	t.Helper()
	composition := newCompositionRun(t, false)
	run := &planHandOffRun{
		ownershipRun: &ownershipRun{compositionRun: composition, log: &orderLog{}},
		t:            t, ids: []string{"repo-1"},
	}
	run.logs = &lockedWriter{buffer: &bytes.Buffer{}}
	run.jr.logger = slog.New(slog.NewTextHandler(run.logs, nil))
	run.provider = func() { run.publish(publishPlanTool, "direct-plan", "") }
	return run
}

func addPlanHandOffRepository(t *testing.T, jr *jobRunner, id string) {
	t.Helper()
	origin := t.TempDir()
	buildWorkspaceGit(t, origin, "init", "-b", "main")
	buildWorkspaceGit(t, origin, "config", "user.email", "runner@example.test")
	buildWorkspaceGit(t, origin, "config", "user.name", "Runner Test")
	writeFixtureFile(t, origin, "README.md", "secondary "+id+"\n", 0o644)
	writeFixtureFile(t, origin, "docs/guide.md", "guide\n", 0o644)
	writeFixtureFile(t, origin, "main.go", "package main\n", 0o644)
	buildWorkspaceGit(t, origin, "add", "-A")
	buildWorkspaceGit(t, origin, "commit", "-m", "fixture")
	repositories := make(map[string]config.RepositoryConfig, len(jr.d.cfg.Repositories)+1)
	for key, value := range jr.d.cfg.Repositories {
		repositories[key] = value
	}
	repositories[id] = config.RepositoryConfig{URL: origin}
	jr.d.cfg.Repositories = repositories
	jr.job.Repositories = append(jr.job.Repositories, v1.JobRepository{
		RepositoryID: id, BaseBranch: "main", BaseSHA: buildWorkspaceGit(t, origin, "rev-parse", "HEAD"),
	})
}

func (run *planHandOffRun) checkout(id string) string {
	return filepath.Join(run.jr.workDir, "repositories", id)
}

func (run *planHandOffRun) evidence() []planCheckoutDigests {
	digests := make([]planCheckoutDigests, len(run.jr.repositories))
	for index, repository := range run.jr.repositories {
		digests[index] = repository.PlanBaseline.current()
	}
	return digests
}

func (run *planHandOffRun) capturePrepared() {
	run.mu.Lock()
	defer run.mu.Unlock()
	run.prepared = run.evidence()
}

func (run *planHandOffRun) execute(t *testing.T) {
	t.Helper()
	run.jr.d.supervisor = planHandOffSupervisor{
		log: run.log,
		onStart: func() {
			run.mu.Lock()
			defer run.mu.Unlock()
			if run.prepared == nil {
				run.prepared = run.evidence()
			}
			run.atStart = run.evidence()
		},
		provider: func() { run.provider() },
	}
	run.jr.run()
}

func (run *planHandOffRun) publish(tool, key, baseRevisionID string) {
	_, err := call(run.t, run.jr, tool, planPublicationArgs(run.t, key, "Plan", "Behavior", baseRevisionID))
	run.mu.Lock()
	defer run.mu.Unlock()
	run.publishErr, run.published = err, true
}

func (run *planHandOffRun) publishResult(t *testing.T) error {
	t.Helper()
	run.mu.Lock()
	defer run.mu.Unlock()
	if !run.published {
		t.Fatalf("the fake provider did not publish (calls %s)", run.log)
	}
	return run.publishErr
}

func (run *planHandOffRun) logText() string {
	run.logs.mu.Lock()
	defer run.logs.mu.Unlock()
	return run.logs.buffer.String()
}

func (run *planHandOffRun) expectPublished(t *testing.T) {
	t.Helper()
	if err := run.publishResult(t); err != nil {
		t.Fatalf("publish: %v (log %s)", err, run.logText())
	}
	events := run.events.byKind("plan_published")
	if len(events) != 1 {
		t.Fatalf("plan events = %d", len(events))
	}
	got, ok := events[0]["repositories"].([]v1.PlanRepositoryAuthority)
	if !ok || len(got) != len(run.ids) || !planRepositoryAuthorityMatches(got, run.jr.job.Repositories) {
		t.Fatalf("plan repositories = %#v", events[0]["repositories"])
	}
	run.expectFinished(t, "succeeded", "")
}

func (run *planHandOffRun) expectPublishRefused(t *testing.T, id string) {
	t.Helper()
	err := run.publishResult(t)
	if err == nil || err.Error() != "plan repository checkout mismatch for "+id {
		t.Fatalf("publish err = %v", err)
	}
	if events := run.events.byKind("plan_published"); len(events) != 0 {
		t.Fatalf("plan events = %d", len(events))
	}
}

func (run *planHandOffRun) expectHandOffRefusal(t *testing.T) {
	t.Helper()
	run.expectCalls(t, "handoff:job remove:a1 reclaim:job")
	run.expectFinished(t, "failed", string(v1.JobFailureWorkspacePreparation))
	if message := str(run.finished(t), "failure_message"); message != "plan checkout changed during ownership hand-off" {
		t.Fatalf("failure_message = %q", message)
	}
	if run.published || run.atStart != nil {
		t.Fatal("the provider started after a changed hand-off")
	}
	if len(run.ownership.reclaims) != 1 {
		t.Fatalf("reclaims = %d", len(run.ownership.reclaims))
	}
}

func entryCtime(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := fileStatKey(info)
	if !ok {
		t.Fatalf("no stat key for %s", path)
	}
	return key.ctime
}

func chmodInPlace(root string) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(name, info.Mode())
	})
}

func bumpCtimes(t *testing.T, root string) {
	t.Helper()
	probe := filepath.Join(root, "main.go")
	before := entryCtime(t, probe)
	deadline := time.Now().Add(5 * time.Second)
	for {
		time.Sleep(2 * time.Millisecond)
		if err := chmodInPlace(root); err != nil {
			t.Error(err)
			return
		}
		if entryCtime(t, probe) != before {
			return
		}
		if time.Now().After(deadline) {
			t.Error("ctime did not change; the clock is too coarse for this test")
			return
		}
	}
}

func (run *planHandOffRun) bumpAll() {
	for _, id := range run.ids {
		bumpCtimes(run.t, run.checkout(id))
	}
}

func (run *planHandOffRun) expectCarriedForward(t *testing.T) {
	t.Helper()
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.prepared) != len(run.ids) || len(run.atStart) != len(run.ids) {
		t.Fatalf("prepared=%d atStart=%d", len(run.prepared), len(run.atStart))
	}
	for index, id := range run.ids {
		if run.atStart[index].Identity != run.prepared[index].Identity {
			t.Fatalf("%s identity changed", id)
		}
		if run.atStart[index].Strict == run.prepared[index].Strict {
			t.Fatalf("%s strict baseline was not carried forward before start", id)
		}
	}
}

func TestRootlessPlanJobCarriesTheBaselineForwardBetweenHandOffAndStart(t *testing.T) {
	for _, repositories := range []int{1, 2} {
		t.Run(strconv.Itoa(repositories)+" repositories", func(t *testing.T) {
			run := newPlanHandOffRun(t, testRootlessMapping, repositories)
			run.beforeHand = run.bumpAll

			run.execute(t)

			run.expectCalls(t, "handoff:job start remove:a1 reclaim:job")
			run.expectCarriedForward(t)
			run.expectPublished(t)
		})
	}
}

func TestRootlessPlanPublicationWithThePrepareTimeBaselineReproducesTheMismatch(t *testing.T) {
	run := newPlanHandOffRun(t, testRootlessMapping, 2)
	run.beforeHand = run.bumpAll
	var causes []string
	run.provider = func() {
		run.mu.Lock()
		prepared := append([]planCheckoutDigests(nil), run.prepared...)
		run.mu.Unlock()
		for index, repository := range run.jr.repositories {
			repository.PlanBaseline = newPlanCheckoutEvidence(prepared[index])
			causes = append(causes, planCheckoutMismatchCause(repository, run.jr.job.Repositories[index].BaseSHA))
		}
		run.publish(publishPlanTool, "carried-plan", "")
	}

	run.execute(t)

	if strings.Join(causes, ",") != "baseline_mismatch,baseline_mismatch" {
		t.Fatalf("prepare-time causes = %v", causes)
	}
	run.expectPublished(t)
}

func TestRootlessPlanJobRefusesAHandOffThatChangesTheCheckout(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, dir string){
		"content": func(t *testing.T, dir string) {
			writeFixtureFile(t, dir, "README.md", "changed during hand-off\n", 0o644)
		},
		"an added file": func(t *testing.T, dir string) {
			writeFixtureFile(t, dir, "added.txt", "added\n", 0o644)
		},
		"a removed file": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "docs", "guide.md")); err != nil {
				t.Fatal(err)
			}
		},
		"a renamed file": func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, "README.md"), filepath.Join(dir, "README.txt")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := newPlanHandOffRun(t, testRootlessMapping, 2)
			run.beforeHand = func() {
				run.bumpAll()
				change(t, run.checkout("repo-2"))
			}

			run.execute(t)

			run.expectHandOffRefusal(t)
			if logs := run.logText(); !strings.Contains(logs, "repository=repo-2 cause=identity_mismatch") {
				t.Fatalf("log = %s", logs)
			}
		})
	}
}

func TestRootlessPlanJobRefusesAHandOffWhoseCheckoutCannotBeRead(t *testing.T) {
	run := newPlanHandOffRun(t, testRootlessMapping, 2)
	closed := filepath.Join(run.checkout("repo-2"), "docs")
	run.beforeHand = func() {
		if err := os.Chmod(closed, 0); err != nil {
			t.Error(err)
		}
	}
	run.ownership.onReclaim = func(isolation.OwnershipRequest) {
		if err := os.Chmod(closed, 0o755); err != nil {
			t.Error(err)
		}
	}

	run.execute(t)

	run.expectHandOffRefusal(t)
	logs := run.logText()
	if !strings.Contains(logs, "repository=repo-2 cause=baseline_unreadable") || strings.Contains(logs, run.jr.workDir) {
		t.Fatalf("log = %s", logs)
	}
}

func TestRootlessPlanJobStillRefusesChangesMadeDuringTheJob(t *testing.T) {
	for name, test := range map[string]struct {
		id     string
		change func(t *testing.T, dir string)
	}{
		"a primary file": {"repo-1", func(t *testing.T, dir string) {
			writeFixtureFile(t, dir, "main.go", "package changed\n", 0o644)
		}},
		"a primary HEAD": {"repo-1", func(t *testing.T, dir string) {
			buildWorkspaceGit(t, dir, "-c", "user.email=a@example.test", "-c", "user.name=A", "commit", "--allow-empty", "-m", "moved")
		}},
		"a secondary file": {"repo-2", func(t *testing.T, dir string) {
			writeFixtureFile(t, dir, "docs/guide.md", "changed guide\n", 0o644)
		}},
		"a secondary permission": {"repo-2", func(t *testing.T, dir string) {
			untilCtimeChanges(t, filepath.Join(dir, "README.md"), func() error {
				return os.Chmod(filepath.Join(dir, "README.md"), 0o600)
			})
		}},
	} {
		t.Run(name, func(t *testing.T) {
			run := newPlanHandOffRun(t, testRootlessMapping, 2)
			run.beforeHand = run.bumpAll
			run.provider = func() {
				test.change(t, run.checkout(test.id))
				run.publish(publishPlanTool, "changed-during-job", "")
			}

			run.execute(t)

			run.expectCarriedForward(t)
			run.expectPublishRefused(t, test.id)
		})
	}
}

func TestRootlessPlanRevisionPublicationUsesTheCarriedBaseline(t *testing.T) {
	for name, change := range map[string]bool{"unchanged": false, "changed": true} {
		t.Run(name, func(t *testing.T) {
			run := newPlanHandOffRun(t, testRootlessMapping, 2)
			run.jr.job.PlanRevisionContext = validDaemonRevisionContext()
			run.beforeHand = run.bumpAll
			run.provider = func() {
				if change {
					writeFixtureFile(t, run.checkout("repo-2"), "README.md", "revised\n", 0o644)
				}
				run.publish(publishPlanRevisionTool, "revision-after-handoff", "revision-1")
			}

			run.execute(t)

			if !change {
				run.expectPublished(t)
				return
			}
			run.expectPublishRefused(t, "repo-2")
		})
	}
}

func TestRootlessPlanPublicationRacingTheCarryForwardIsRaceFree(t *testing.T) {
	run := newPlanHandOffRun(t, testRootlessMapping, 2)
	var racing sync.WaitGroup
	stop := make(chan struct{})
	run.beforeHand = func() {
		run.bumpAll()
		racing.Add(1)
		go func() {
			defer racing.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = run.jr.verifyPlanRepositoryAuthority(context.Background())
				}
			}
		}()
	}
	run.provider = func() {
		close(stop)
		racing.Wait()
		run.publish(publishPlanTool, "after-race", "")
	}

	run.execute(t)

	run.expectCarriedForward(t)
	run.expectPublished(t)
}

func TestPlanJobsWithoutAHandOffKeepThePrepareTimeBaseline(t *testing.T) {
	for name, newRun := range map[string]func(t *testing.T) *planHandOffRun{
		"direct isolation": newDirectPlanHandOffRun,
		"identity mapping": func(t *testing.T) *planHandOffRun {
			return newPlanHandOffRun(t, testIdentityMapping, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := newRun(t)
			var prepared []planCheckoutDigests
			run.jr.compositionHook = func(context.Context) error {
				prepared = run.evidence()
				run.bumpAll()
				return nil
			}

			run.execute(t)

			run.expectCalls(t, "start")
			if len(prepared) != 1 || len(run.atStart) != 1 || run.atStart[0] != prepared[0] {
				t.Fatalf("baseline at start %+v, prepared %+v", run.atStart, prepared)
			}
			run.expectPublishRefused(t, "repo-1")
			if logs := run.logText(); !strings.Contains(logs, "repository=repo-1 cause=baseline_mismatch") {
				t.Fatalf("log = %s", logs)
			}
		})
	}
}

func TestBuildJobsSkipThePlanCarryForward(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.repositories = []checkedOutRepository{{ID: "repo-1", WorkDir: filepath.Join(t.TempDir(), "missing")}}
	if err := jr.carryPlanCheckoutForward(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanCarryForwardRefusesAMissingPrepareTimeBaseline(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	logs := &lockedWriter{buffer: &bytes.Buffer{}}
	jr.logger = slog.New(slog.NewTextHandler(logs, nil))
	jr.repositories = []checkedOutRepository{{ID: "repo-1", WorkDir: emptyPlanCheckout(t)}}
	if err := jr.carryPlanCheckoutForward(); err == nil || err.Error() != planCheckoutHandOffFailure {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(logs.buffer.String(), "repository=repo-1 cause=baseline_missing") {
		t.Fatalf("log = %s", logs.buffer.String())
	}
}

func TestRootlessPlanJobLeavesNoAgentWorktreeCleanupWarning(t *testing.T) {
	run := newPlanHandOffRun(t, testRootlessMapping, 2)
	run.beforeHand = run.bumpAll

	run.execute(t)

	run.expectPublished(t)
	if logs := run.logText(); strings.Contains(logs, "agent worktree cleanup") {
		t.Fatalf("log = %s", logs)
	}
}
