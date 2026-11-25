package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/supervisor"
)

type agentDuringWaitProcess struct {
	ownershipTestProcess
	done chan struct{}
}

func (process agentDuringWaitProcess) Wait() error {
	<-process.done
	return nil
}

type agentDuringWaitSupervisor struct {
	*ownershipTestSupervisor
	work func()
}

func (value *agentDuringWaitSupervisor) Start(ctx context.Context, spec supervisor.Spec) (supervisor.Process, error) {
	if _, err := value.ownershipTestSupervisor.Start(ctx, spec); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		value.work()
	}()
	return agentDuringWaitProcess{done: done}, nil
}

func TestBuildJobRunCancelledDuringFinalRedeployRunsTheWholeFinish(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	jr := run.jr
	jr.job.Kind = v1.JobKindBuild
	jr.d.cfg.JobNetwork = "restricted"
	jr.d.cfg.JobNetworkName = "job-network"
	controller := newFakeJobNetworkController()
	jr.d.previewHostController = controller
	jr.d.previewTunnelAdvertised.Store(true)
	if _, err := jr.d.previewHostRepositoryManager().Register(context.Background(), jr.d.cfg.Repositories["repo-1"].URL); err != nil {
		t.Fatal(err)
	}
	jr.d.jobMu.Lock()
	jr.d.activeJob = jr
	jr.d.jobMu.Unlock()
	requests := 0
	respondToPreviewDeployRequests(jr, run.events, func(event map[string]any) {
		requests++
		if requests == 1 {
			deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, time.Now().Add(time.Hour).Truncate(time.Millisecond))
			return
		}
		jr.d.cancelActiveJob("test cancel")
	})
	var firstReply previewDeployToolReply
	agent := &agentDuringWaitSupervisor{ownershipTestSupervisor: run.sup, work: func() {
		result, err := jr.handleToolCall(context.Background(), mcpbridge.DeployPreviewTool, []byte(`{}`))
		if err != nil {
			t.Errorf("deploy preview during the run: %v", err)
			return
		}
		firstReply, _ = result.(previewDeployToolReply)
		if err := os.WriteFile(filepath.Join(run.checkout, "final.txt"), []byte("final\n"), 0o600); err != nil {
			t.Error(err)
		}
		jr.reviewReadyIntent = &reviewReadyIntent{
			summary: "Ready for review", limitations: []string{}, idempotencyKey: "review-ready-run-finish",
		}
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		jr.d.supervisor = agent
		run.compositionRun.execute(t)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("run did not return after cancellation during the final redeploy")
	}
	assertPreviewDeployReply(t, firstReply, v1.PreviewJobDeployStatusDeployed, "")
	if requests != 2 {
		t.Fatalf("deploy requests = %d, want the agent deploy and the final redeploy", requests)
	}
	run.expectFinished(t, string(v1.JobFinishedCancelled), string(v1.JobFailureCancelled))
	assertNoPreviewEvidence(t, run.events)
	if events := run.events.byKind("review_ready"); len(events) != 0 {
		t.Fatalf("review_ready events = %v", events)
	}
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
	container := isolation.ContainerName(jr.d.runnerID, jr.job.AttemptID)
	if got := controller.attachCalls(); len(got) != 1 || got[0].attemptID != jr.job.AttemptID || got[0].container != container {
		t.Fatalf("attaches = %+v", got)
	}
	if got := controller.detachCalls(); !slices.Equal(got, []string{jr.job.AttemptID}) {
		t.Fatalf("detaches = %v", got)
	}
	jr.previewNetworkMu.Lock()
	closed := jr.previewNetworkClosed
	jr.previewNetworkMu.Unlock()
	if !closed {
		t.Fatal("job preview network was left open for later attaches")
	}
	if handOffs, reclaims := len(run.ownership.handOffs), len(run.ownership.reclaims); handOffs != 1 || reclaims != 1 {
		t.Fatalf("ownership hand-offs = %d reclaims = %d (calls %s)", handOffs, reclaims, run.log)
	}
	if calls := run.log.String(); !strings.HasPrefix(calls, "handoff:job start") || !strings.HasSuffix(calls, "reclaim:job") {
		t.Fatalf("ownership calls = %q", calls)
	}
	if jr.ctx.Err() == nil {
		t.Fatal("job context is still live after the run")
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err == nil {
		t.Fatal("lease is still held after the run")
	}
}
