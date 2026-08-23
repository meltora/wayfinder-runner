package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type recordingProcess struct {
	mu        sync.Mutex
	killCount int
	killErr   error
}

func (*recordingProcess) PID() int         { return 123 }
func (*recordingProcess) Wait() error      { return context.Canceled }
func (*recordingProcess) Interrupt() error { return nil }
func (process *recordingProcess) Kill() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.killCount++
	return process.killErr
}

func (process *recordingProcess) kills() int {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.killCount
}

func controlCommandForJob(job v1.JobSpec, action v1.JobControlAction, now time.Time) v1.CommandPayload {
	command := v1.CommandPayload{
		Command: v1.CmdControlJob, ControlID: "control-1", Action: action,
		Reason: "operator requested lifecycle control", RequestedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		JobID: job.JobID, AttemptID: job.AttemptID, LeaseID: job.LeaseID,
		ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest, FencingToken: job.FencingToken,
		AgentRunAuthority: job.AgentRunAuthority,
	}
	operation := command.JobControlOperation()
	command.CommandDigest = v1.JobControlCommandDigest(operation)
	return command
}

func prepareControlJob(t *testing.T) (*jobRunner, *capturedEvents, *recordingProcess) {
	t.Helper()
	jr, events := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	repositoryDir := filepath.Join(jr.workDir, "repository")
	if err := os.MkdirAll(repositoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "runner@example.test"},
		{"config", "user.name", "Runner"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repositoryDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repositoryDir, "README.md"), []byte("checkpoint\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = repositoryDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	cmd = exec.Command("git", "commit", "-qm", "checkpoint")
	cmd.Dir = repositoryDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	jr.repositories = []checkedOutRepository{{ID: "repo-1", Branch: "main", WorkDir: repositoryDir, Primary: true}}
	process := &recordingProcess{}
	jr.proc = process
	jr.d.jobMu.Lock()
	jr.d.activeJob = jr
	jr.d.jobMu.Unlock()
	return jr, events, process
}

func TestPauseControlPersistsCheckpointBeforeStoppingProcess(t *testing.T) {
	jr, events, process := prepareControlJob(t)
	command := controlCommandForJob(jr.job, v1.JobControlPause, time.Now().UTC())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-command-1", command), command); err != nil {
		t.Fatalf("pause control failed: %v", err)
	}
	if process.kills() != 1 {
		t.Fatalf("kill count=%d", process.kills())
	}
	checkpoint, err := jr.d.collectors.CheckpointForAttempt(jr.job.JobID, jr.job.AttemptID, jr.job.FencingToken)
	if err != nil || checkpoint.TrustedRecovery == nil {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	if got := events.byKind("checkpoint"); len(got) != 1 || got[0]["recovery_checkpoint"] == nil || got[0]["agent_run_authority"] == nil {
		t.Fatalf("checkpoint events=%v", got)
	}
	if got := events.byKind("job_control_result"); len(got) != 1 || got[0]["outcome"] != string(v1.JobControlApplied) {
		t.Fatalf("control results=%v", got)
	}
}

func TestControlRejectsStaleAuthorityBeforeProcessCall(t *testing.T) {
	jr, events, process := prepareControlJob(t)
	command := controlCommandForJob(jr.job, v1.JobControlCancel, time.Now().UTC())
	command.FencingToken++
	operation := command.JobControlOperation()
	command.CommandDigest = v1.JobControlCommandDigest(operation)
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-command-2", command), command); err != nil {
		t.Fatalf("valid-shaped stale command should produce a result: %v", err)
	}
	if process.kills() != 0 {
		t.Fatal("stale control reached supervised process")
	}
	got := events.byKind("job_control_result")
	if len(got) != 1 || got[0]["outcome"] != string(v1.JobControlRejected) || got[0]["reason_code"] != string(v1.JobControlReasonStaleAuthority) {
		t.Fatalf("control results=%v", got)
	}
}

func TestControlCommandBoundaryRejectsSmuggledFields(t *testing.T) {
	jr, _, _ := prepareControlJob(t)
	command := controlCommandForJob(jr.job, v1.JobControlCancel, time.Now().UTC())
	command.QuestionID = "smuggled"
	if err := validateJobControlCommandBoundary(command, time.Now()); err == nil {
		t.Fatal("control command accepted an unrelated authority field")
	}
	legacy := v1.CommandPayload{Command: v1.CmdResumeRunner, ControlID: "smuggled"}
	if err := validateJobControlCommandBoundary(legacy, time.Now()); err == nil {
		t.Fatal("legacy command accepted control authority")
	}
}

func TestJobControlResultDurablyConvergesCommandRecovery(t *testing.T) {
	jr, _, process := prepareControlJob(t)
	command := controlCommandForJob(jr.job, v1.JobControlCancel, time.Now().UTC())
	envelope := commandEnvelope("control-command-recovery", command)
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if process.kills() != 1 {
		t.Fatalf("initial kill count=%d", process.kills())
	}
	persistUnexecutedCommand(t, jr.d, envelope, "process-old")
	jr.d.commandOwner = "process-new"
	jr.d.jobMu.Lock()
	jr.d.activeJob = nil // The old process retired the attempt after applying control.
	jr.d.jobMu.Unlock()
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if receipt := commandReceipt(t, jr.d, envelope); receipt.State != "accepted" {
		t.Fatalf("recovered control receipt=%+v", receipt)
	}
	if process.kills() != 1 {
		t.Fatalf("recovery repeated process stop: %d", process.kills())
	}
}

func TestCancelControlProducesTerminalCancellationOutcome(t *testing.T) {
	jr, _, _ := prepareControlJob(t)
	command := controlCommandForJob(jr.job, v1.JobControlCancel, time.Now().UTC())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-command-cancel", command), command); err != nil {
		t.Fatal(err)
	}
	outcome := jr.startupFailureOutcome(v1.JobFailureAgentStart, "must not win")
	if outcome.State != v1.JobFinishedCancelled || outcome.FailureCode != v1.JobFailureCancelled {
		t.Fatalf("cancel terminal outcome=%+v", outcome)
	}
}

func TestJobControlAlreadyAppliedAndConflictingTerminalResults(t *testing.T) {
	jr, events, process := prepareControlJob(t)
	now := time.Now().UTC()
	first := controlCommandForJob(jr.job, v1.JobControlCancel, now)
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-first", first), first); err != nil {
		t.Fatal(err)
	}
	repeated := controlCommandForJob(jr.job, v1.JobControlCancel, now)
	repeated.ControlID = "control-repeated"
	repeated.CommandDigest = v1.JobControlCommandDigest(repeated.JobControlOperation())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-repeated", repeated), repeated); err != nil {
		t.Fatal(err)
	}
	conflict := controlCommandForJob(jr.job, v1.JobControlPause, now)
	conflict.ControlID = "control-conflict"
	conflict.CommandDigest = v1.JobControlCommandDigest(conflict.JobControlOperation())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-conflict", conflict), conflict); err != nil {
		t.Fatal(err)
	}
	if process.kills() != 1 {
		t.Fatalf("process stopped %d times", process.kills())
	}
	results := events.byKind("job_control_result")
	if len(results) != 3 || results[1]["outcome"] != string(v1.JobControlAlreadyApplied) ||
		results[1]["reason_code"] != string(v1.JobControlReasonAlreadyCancelled) ||
		results[2]["reason_code"] != string(v1.JobControlReasonTerminalAttempt) {
		t.Fatalf("results=%v", results)
	}
}

func TestConcurrentControlsCallSupervisorExactlyOnce(t *testing.T) {
	jr, events, process := prepareControlJob(t)
	now := time.Now().UTC()
	commands := []v1.CommandPayload{
		controlCommandForJob(jr.job, v1.JobControlCancel, now),
		controlCommandForJob(jr.job, v1.JobControlCancel, now),
	}
	commands[1].ControlID = "control-concurrent-2"
	commands[1].CommandDigest = v1.JobControlCommandDigest(commands[1].JobControlOperation())
	var wg sync.WaitGroup
	errorsSeen := make(chan error, len(commands))
	for index, command := range commands {
		wg.Add(1)
		go func(index int, command v1.CommandPayload) {
			defer wg.Done()
			errorsSeen <- jr.d.executeCommand(context.Background(), commandEnvelope(fmt.Sprintf("control-concurrent-%d", index), command), command)
		}(index, command)
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if process.kills() != 1 {
		t.Fatalf("process stopped %d times", process.kills())
	}
	results := events.byKind("job_control_result")
	if len(results) != 2 {
		t.Fatalf("results=%v", results)
	}
	outcomes := map[any]int{}
	for _, result := range results {
		outcomes[result["outcome"]]++
	}
	if outcomes[string(v1.JobControlApplied)] != 1 || outcomes[string(v1.JobControlAlreadyApplied)] != 1 {
		t.Fatalf("outcomes=%v", outcomes)
	}
}

func TestControlRejectsAttemptWhoseCompletionStarted(t *testing.T) {
	jr, events, process := prepareControlJob(t)
	jr.procMu.Lock()
	jr.completionStarted = true
	jr.procMu.Unlock()
	if err := jr.stopForControl(v1.JobControlCancel); !errors.Is(err, errControlAttemptCompleted) {
		t.Fatalf("stop after completion error=%v", err)
	}
	command := controlCommandForJob(jr.job, v1.JobControlCancel, time.Now().UTC())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-terminal", command), command); err != nil {
		t.Fatal(err)
	}
	if process.kills() != 0 {
		t.Fatal("terminal attempt reached supervisor")
	}
	results := events.byKind("job_control_result")
	if len(results) != 1 || results[0]["reason_code"] != string(v1.JobControlReasonTerminalAttempt) {
		t.Fatalf("results=%v", results)
	}
}

func TestPauseRefusesToStopWithoutTrustedCheckpoint(t *testing.T) {
	jr, events := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	process := &recordingProcess{}
	jr.proc = process
	jr.d.jobMu.Lock()
	jr.d.activeJob = jr
	jr.d.jobMu.Unlock()
	command := controlCommandForJob(jr.job, v1.JobControlPause, time.Now().UTC())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-no-checkpoint", command), command); err != nil {
		t.Fatal(err)
	}
	if process.kills() != 0 {
		t.Fatal("pause stopped process without a trusted checkpoint")
	}
	results := events.byKind("job_control_result")
	if len(results) != 1 || results[0]["reason_code"] != string(v1.JobControlReasonCheckpointUnavailable) {
		t.Fatalf("results=%v", results)
	}
}

func TestControlStopFailureIsControlledAndDoesNotCancelContext(t *testing.T) {
	jr, events, process := prepareControlJob(t)
	process.killErr = errors.New("stop failed")
	command := controlCommandForJob(jr.job, v1.JobControlCancel, time.Now().UTC())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-stop-failed", command), command); err != nil {
		t.Fatal(err)
	}
	if jr.ctx.Err() != nil {
		t.Fatal("failed supervisor stop cancelled the job context")
	}
	results := events.byKind("job_control_result")
	if len(results) != 1 || results[0]["reason_code"] != string(v1.JobControlReasonRunnerInternalError) {
		t.Fatalf("results=%v", results)
	}
}

func TestControlRejectsWhenNoAttemptIsActive(t *testing.T) {
	jr, _ := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	command := controlCommandForJob(jr.job, v1.JobControlCancel, time.Now().UTC())
	if err := jr.d.executeCommand(context.Background(), commandEnvelope("control-terminal", command), command); err == nil {
		t.Fatal("control without an active attempt was accepted")
	}
}

func TestRecoveryPatchDigestCoversTrackedAndUntrackedContent(t *testing.T) {
	jr, _, _ := prepareControlJob(t)
	repository := jr.repositories[0]
	if clean, err := repositoryPatchDigest(context.Background(), repository.WorkDir); err != nil || clean != "" {
		t.Fatalf("clean digest=%q err=%v", clean, err)
	}
	if err := os.WriteFile(filepath.Join(repository.WorkDir, "README.md"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.WorkDir, "new.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := repositoryPatchDigest(context.Background(), repository.WorkDir)
	if err != nil || len(digest) != 64 {
		t.Fatalf("dirty digest=%q err=%v", digest, err)
	}
	repository.Branch = "other"
	if _, err := snapshotRepository(context.Background(), repository); err == nil {
		t.Fatal("snapshot accepted a mismatched branch")
	}
}

func TestGitOutputBytesLimitedRejectsOversizedOutput(t *testing.T) {
	jr, _, _ := prepareControlJob(t)
	if _, err := gitOutputBytesLimited(context.Background(), jr.repositories[0].WorkDir, 1, "rev-parse", "HEAD"); err == nil {
		t.Fatal("oversized git output accepted")
	}
}

func TestAwaitControlResultHandlesAbsentAndPendingBarrier(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.awaitControlResult()
	jr.procMu.Lock()
	jr.controlResultDone = make(chan struct{})
	jr.procMu.Unlock()
	done := make(chan struct{})
	go func() {
		jr.awaitControlResult()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("pending control barrier returned early")
	case <-time.After(10 * time.Millisecond):
	}
	jr.signalControlResultReady()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control barrier did not release")
	}
}
