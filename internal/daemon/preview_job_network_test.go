package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/previewhost"
)

const previewDeployToolPreviewID = "preview-job-1"

type fakeJobNetworkAttach struct {
	previewID, attemptID, container string
}

type fakeJobNetworkController struct {
	*fakePreviewHostController
	mu              sync.Mutex
	attaches        []fakeJobNetworkAttach
	detaches        []string
	sweeps          int
	sweepCandidates []string
	kept            []string
	removed         []string
	attachErr       error
	attachCtxErrs   []error
	attachStarted   chan struct{}
	attachRelease   chan struct{}
	attachDone      chan error
	detachErr       error
}

var _ previewhost.JobNetworkAttacher = (*fakeJobNetworkController)(nil)

func newFakeJobNetworkController() *fakeJobNetworkController {
	return &fakeJobNetworkController{fakePreviewHostController: &fakePreviewHostController{}}
}

func (c *fakeJobNetworkController) AttachJob(ctx context.Context, previewID, attemptID, jobContainer string) error {
	c.mu.Lock()
	c.attaches = append(c.attaches, fakeJobNetworkAttach{previewID, attemptID, jobContainer})
	c.attachCtxErrs = append(c.attachCtxErrs, ctx.Err())
	started, release, done, err := c.attachStarted, c.attachRelease, c.attachDone, c.attachErr
	c.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if done != nil {
		done <- err
	}
	return err
}

func (c *fakeJobNetworkController) DetachJob(_ context.Context, attemptID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detaches = append(c.detaches, attemptID)
	return c.detachErr
}

type sweepOnlyPreviewHostController struct {
	*fakePreviewHostController
	sweeps int
}

func (c *sweepOnlyPreviewHostController) SweepJobNetworks(context.Context, func(string) bool) error {
	c.sweeps++
	return nil
}

func (c *fakeJobNetworkController) SweepJobNetworks(_ context.Context, keep func(attemptID string) bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweeps++
	for _, attemptID := range c.sweepCandidates {
		if keep(attemptID) {
			c.kept = append(c.kept, attemptID)
		} else {
			c.removed = append(c.removed, attemptID)
		}
	}
	return nil
}

func (c *fakeJobNetworkController) attachCalls() []fakeJobNetworkAttach {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.attaches)
}

func (c *fakeJobNetworkController) detachCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.detaches)
}

func jobNetworkDeployFixture(t *testing.T) (*jobRunner, *capturedEvents, *fakeJobNetworkController) {
	t.Helper()
	jr, captured, _ := deployPreviewFixture(t)
	jr.d.runnerID = "runner-1"
	controller := newFakeJobNetworkController()
	jr.d.previewHostController = controller
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		deployPreviewResult(jr, event, time.Now().Add(time.Hour))
	})
	return jr, captured, controller
}

func TestDeployPreviewAttachesJobNetworkAfterDeploy(t *testing.T) {
	jr, _, controller := jobNetworkDeployFixture(t)
	reply := callDeployPreview(t, jr)
	assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusDeployed, "")
	want := []fakeJobNetworkAttach{{
		previewID: previewDeployToolPreviewID, attemptID: jr.job.AttemptID,
		container: isolation.ContainerName("runner-1", jr.job.AttemptID),
	}}
	if got := controller.attachCalls(); !slices.Equal(got, want) {
		t.Fatalf("attaches = %+v want %+v", got, want)
	}
}

func TestDeployPreviewAttachFailureKeepsDeployedReply(t *testing.T) {
	jr, _, controller := jobNetworkDeployFixture(t)
	controller.attachErr = errors.New("docker unavailable")
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	if got := controller.attachCalls(); len(got) != 1 {
		t.Fatalf("attaches = %+v", got)
	}
}

func TestDeployPreviewSkipsAttachForDirectIsolation(t *testing.T) {
	for name, tc := range map[string]struct {
		configure func(*jobRunner)
		code      string
	}{
		"direct isolation": {func(jr *jobRunner) { jr.d.cfg.Isolation = "direct" }, previewJobDeployCodeNetworkUnsupported},
		"network none":     {func(jr *jobRunner) { jr.d.cfg.JobNetwork = "none" }, previewJobDeployCodeNetworkUnsupported},
		"no network name":  {func(jr *jobRunner) { jr.d.cfg.JobNetworkName = "" }, previewJobDeployCodeNetworkUnsupported},
		"codex reach closed": {func(jr *jobRunner) {
			jr.d.cfg.Provider = "codex"
			jr.previewReachOpen.Store(false)
		}, previewJobDeployCodeNetworkUnsupported},
		"reach closed":        {func(jr *jobRunner) { jr.previewReachOpen.Store(false) }, previewJobDeployCodeCapabilityMissing},
		"claude reach closed": {func(jr *jobRunner) { jr.d.cfg.Provider = "claude"; jr.previewReachOpen.Store(false) }, previewJobDeployCodeCapabilityMissing},
	} {
		t.Run(name, func(t *testing.T) {
			jr, captured, controller := jobNetworkDeployFixture(t)
			tc.configure(jr)
			jr.previewJobDeployed(previewJobDeployOutcome{
				Status: v1.PreviewJobDeployStatusDeployed, PreviewID: previewDeployToolPreviewID,
			})
			assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusRefused, tc.code)
			assertNoPreviewDeployRequest(t, jr, captured)
			if got := controller.attachCalls(); len(got) != 0 {
				t.Fatalf("attaches = %+v", got)
			}
		})
	}
}

func TestDeployPreviewAttachOutlivesTheToolCallContext(t *testing.T) {
	jr, captured, controller := jobNetworkDeployFixture(t)
	callCtx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		cancelCall()
		deployPreviewResult(jr, event, time.Now().Add(time.Hour))
	})
	result, err := jr.handleToolCall(callCtx, mcpbridge.DeployPreviewTool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := result.(previewDeployToolReply)
	assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusDeployed, "")
	if callCtx.Err() == nil {
		t.Fatal("tool call context was not cancelled before the attach")
	}
	controller.mu.Lock()
	ctxErrs := slices.Clone(controller.attachCtxErrs)
	controller.mu.Unlock()
	if len(ctxErrs) != 1 || ctxErrs[0] != nil {
		t.Fatalf("attach context errors = %v", ctxErrs)
	}
}

func TestDeployPreviewAttachStopsWhenTheJobContextEnds(t *testing.T) {
	jr, _, controller := jobNetworkDeployFixture(t)
	controller.attachStarted = make(chan struct{})
	controller.attachRelease = make(chan struct{})
	controller.attachDone = make(chan error, 1)
	go jr.previewJobDeployed(previewJobDeployOutcome{
		Status: v1.PreviewJobDeployStatusDeployed, PreviewID: previewDeployToolPreviewID,
	})
	select {
	case <-controller.attachStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("attach did not start")
	}
	jr.cancel()
	select {
	case err := <-controller.attachDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("job context end did not stop the attach")
	}
}

func TestCancelRecoveredLeaseDetachesJobNetwork(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	controller := newFakeJobNetworkController()
	d.previewHostController = controller
	current := *d.leases.Current()
	if err := d.journal.SaveLease(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	d.recoveredLease.Store(true)
	if err := d.cancelJob(context.Background(), current.AttemptID, "test"); err != nil {
		t.Fatal(err)
	}
	if got := controller.detachCalls(); !slices.Equal(got, []string{current.AttemptID}) {
		t.Fatalf("detaches = %v", got)
	}
	if lease, err := d.journal.LoadLease(context.Background()); err != nil || lease != nil {
		t.Fatalf("lease after cancel = %+v err = %v", lease, err)
	}
	if d.recoveredLease.Load() {
		t.Fatal("recovered lease flag survived the cancel")
	}
}

func TestCancelRecoveredLeaseReleasesLeaseWhenDetachFails(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	var logs bytes.Buffer
	d.logger = slog.New(slog.NewTextHandler(&logs, nil))
	controller := newFakeJobNetworkController()
	controller.detachErr = errors.New("docker unavailable")
	d.previewHostController = controller
	current := *d.leases.Current()
	if err := d.journal.SaveLease(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	d.recoveredLease.Store(true)
	if err := d.cancelJob(context.Background(), current.AttemptID, "test"); err != nil {
		t.Fatalf("cancel failed although only the job network detach failed: %v", err)
	}
	if got := controller.detachCalls(); !slices.Equal(got, []string{current.AttemptID}) {
		t.Fatalf("detaches = %v", got)
	}
	if lease, err := d.journal.LoadLease(context.Background()); err != nil || lease != nil {
		t.Fatalf("lease after failed detach = %+v err = %v", lease, err)
	}
	if d.leases.Current() != nil || d.recoveredLease.Load() {
		t.Fatal("failed detach kept the recovered lease")
	}
	network := previewhost.JobNetworkName(d.runnerID, current.AttemptID)
	for _, want := range []string{"level=WARN", "network=" + network, "docker unavailable"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log %q lacks %q", logs.String(), want)
		}
	}
	controller.sweepCandidates = []string{current.AttemptID}
	d.sweepJobPreviewNetworksAtStartup(context.Background())
	controller.mu.Lock()
	removed, kept := slices.Clone(controller.removed), slices.Clone(controller.kept)
	controller.mu.Unlock()
	if !slices.Equal(removed, []string{current.AttemptID}) || len(kept) != 0 {
		t.Fatalf("startup sweep removed = %v kept = %v", removed, kept)
	}
}

func TestDeployPreviewRefusesJobNetworkForCodex(t *testing.T) {
	jr, captured, controller := jobNetworkDeployFixture(t)
	jr.d.cfg.Provider = "codex"
	if jr.previewReachAllowed() {
		t.Fatal("codex job opened preview reach")
	}
	jr.previewReachOpen.Store(jr.previewReachAllowed())
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusRefused, previewJobDeployCodeNetworkUnsupported)
	assertNoPreviewDeployRequest(t, jr, captured)
	if got := controller.attachCalls(); len(got) != 0 {
		t.Fatalf("attaches = %+v", got)
	}
}

func TestPreviewReachOpensOnlyWhenTheDeployToolIsOffered(t *testing.T) {
	for name, tc := range map[string]struct {
		configure func(*jobRunner)
		want      bool
	}{
		"offered claude":   {func(*jobRunner) {}, true},
		"offered opencode": {func(jr *jobRunner) { jr.d.cfg.Provider = "opencode" }, true},
		"codex":            {func(jr *jobRunner) { jr.d.cfg.Provider = "codex" }, false},
		"no hosting":       {func(jr *jobRunner) { jr.d.previewHostController = nil }, false},
		"no tunnel":        {func(jr *jobRunner) { jr.d.previewTunnelAdvertised.Store(false) }, false},
		"plan job":         {func(jr *jobRunner) { jr.job.Kind = v1.JobKindPlan }, false},
		"direct":           {func(jr *jobRunner) { jr.d.cfg.Isolation = "direct" }, false},
		"network none":     {func(jr *jobRunner) { jr.d.cfg.JobNetwork = "none" }, false},
		"no network name":  {func(jr *jobRunner) { jr.d.cfg.JobNetworkName = "" }, false},
	} {
		t.Run(name, func(t *testing.T) {
			jr, _, _ := jobNetworkDeployFixture(t)
			jr.d.cfg.Provider = "claude"
			tc.configure(jr)
			if got := jr.previewReachAllowed(); got != tc.want {
				t.Fatalf("reach = %v want %v", got, tc.want)
			}
		})
	}
}

func TestDeployPreviewDoesNotInterleaveWithRunningAttach(t *testing.T) {
	jr, _, controller := jobNetworkDeployFixture(t)
	controller.attachStarted = make(chan struct{})
	controller.attachRelease = make(chan struct{})
	firstReply := make(chan previewDeployToolReply, 1)
	go func() {
		result, _ := jr.handleToolCall(context.Background(), mcpbridge.DeployPreviewTool, json.RawMessage(`{}`))
		reply, _ := result.(previewDeployToolReply)
		firstReply <- reply
	}()
	select {
	case <-controller.attachStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("attach did not start")
	}
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusRefused, previewJobDeployCodeInFlight)
	if got := len(journaledPreviewDeployRequests(t, jr)); got != 1 {
		t.Fatalf("a call during attach journaled %d requests", got)
	}
	controller.mu.Lock()
	release := controller.attachRelease
	controller.attachStarted, controller.attachRelease = nil, nil
	controller.mu.Unlock()
	close(release)
	select {
	case reply := <-firstReply:
		assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusDeployed, "")
	case <-time.After(10 * time.Second):
		t.Fatal("first call did not finish")
	}
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	if got := controller.attachCalls(); len(got) != 2 {
		t.Fatalf("attaches = %+v", got)
	}
}

func TestPreviewJobNetworkIsNotAttachedAfterJobEnd(t *testing.T) {
	jr, _, controller := jobNetworkDeployFixture(t)
	jr.detachPreviewJobNetwork()
	jr.previewJobDeployed(previewJobDeployOutcome{
		Status: v1.PreviewJobDeployStatusDeployed, PreviewID: previewDeployToolPreviewID,
	})
	if got := controller.attachCalls(); len(got) != 0 {
		t.Fatalf("attaches after job end = %+v", got)
	}
	if got := controller.detachCalls(); !slices.Equal(got, []string{jr.job.AttemptID}) {
		t.Fatalf("detaches = %v", got)
	}
}

func TestJobEndDetachesPreviewJobNetwork(t *testing.T) {
	jr, events := testJobRunner(t)
	controller := newFakeJobNetworkController()
	jr.d.previewHostController = controller
	jr.workDir = filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(jr.workDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	jr.workDir = filepath.Join(jr.workDir, "job")
	jr.run()
	if finished := events.byKind("job_finished"); len(finished) != 1 {
		t.Fatalf("job_finished = %v", finished)
	}
	if got := controller.detachCalls(); !slices.Equal(got, []string{jr.job.AttemptID}) {
		t.Fatalf("detaches = %v", got)
	}
}

func TestStartupSweepKeepsTheJournalLiveAttempt(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	controller := newFakeJobNetworkController()
	controller.sweepCandidates = []string{jr.job.AttemptID, "stale-attempt"}
	d.previewHostController = controller
	if err := d.journal.SaveLease(context.Background(), *d.leases.Current()); err != nil {
		t.Fatal(err)
	}
	d.sweepJobPreviewNetworksAtStartup(context.Background())
	if controller.sweeps != 1 || !slices.Equal(controller.kept, []string{jr.job.AttemptID}) || !slices.Equal(controller.removed, []string{"stale-attempt"}) {
		t.Fatalf("sweeps=%d kept=%v removed=%v", controller.sweeps, controller.kept, controller.removed)
	}
}

func TestStartupSweepWithoutLiveAttemptRemovesAll(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	controller := newFakeJobNetworkController()
	controller.sweepCandidates = []string{jr.job.AttemptID, "stale-attempt"}
	d.previewHostController = controller
	d.sweepJobPreviewNetworksAtStartup(context.Background())
	if controller.sweeps != 1 || len(controller.kept) != 0 || len(controller.removed) != 2 {
		t.Fatalf("sweeps=%d kept=%v removed=%v", controller.sweeps, controller.kept, controller.removed)
	}
}

func TestStartupSweepSkipsWithoutJobNetworkSupport(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	if err := d.journal.SaveLease(context.Background(), *d.leases.Current()); err != nil {
		t.Fatal(err)
	}
	d.previewHostController = nil
	d.sweepJobPreviewNetworksAtStartup(context.Background())
	sweepOnly := &sweepOnlyPreviewHostController{fakePreviewHostController: &fakePreviewHostController{}}
	if _, ok := previewhost.Controller(sweepOnly).(previewhost.JobNetworkAttacher); ok {
		t.Fatal("sweep-only controller satisfies JobNetworkAttacher")
	}
	d.previewHostController = sweepOnly
	d.sweepJobPreviewNetworksAtStartup(context.Background())
	if sweepOnly.sweeps != 0 {
		t.Fatalf("sweeps = %d without job network support", sweepOnly.sweeps)
	}
}
