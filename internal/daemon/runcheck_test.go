package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
)

func initRunCheckRepo(t *testing.T, dir string) {
	t.Helper()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.test")
	git("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "initial")
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD^{commit}")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func runCheckFixture(t *testing.T) (*jobRunner, *capturedEvents) {
	t.Helper()
	jr, cap := testJobRunner(t)
	primaryDir := t.TempDir()
	secondaryDir := t.TempDir()
	initRunCheckRepo(t, primaryDir)
	initRunCheckRepo(t, secondaryDir)
	jr.repositories = []checkedOutRepository{
		{ID: "primary", WorkDir: primaryDir, Primary: true},
		{ID: "secondary", WorkDir: secondaryDir},
	}
	jr.job.RequiredChecks = []string{"unit-check"}
	jr.d.cfg.Checks = map[string]config.CheckConfig{
		"unit-check": {Command: "true"},
	}
	return jr, cap
}

func TestRunCheckCapabilityAdvertisedForBuildJobsOnly(t *testing.T) {
	jr, _ := runCheckFixture(t)
	if buildCaps := capabilitiesForJob(v1.JobKindBuild); !slices.Contains(buildCaps, "platform_run_check") {
		t.Fatalf("build capabilities missing platform_run_check: %v", buildCaps)
	}
	if planCaps := capabilitiesForJob(v1.JobKindPlan); slices.Contains(planCaps, "platform_run_check") {
		t.Fatalf("plan capabilities include platform_run_check: %v", planCaps)
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "ghost-check")
	ctxRes, err := call(t, jr, "platform_get_job_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	ctxMap := ctxRes.(map[string]any)
	runnable, ok := ctxMap["runnable_checks"].([]string)
	if !ok {
		t.Fatalf("runnable_checks missing or has unexpected type: %T", ctxMap["runnable_checks"])
	}
	if !slices.Contains(runnable, "unit-check") {
		t.Fatalf("runnable_checks = %v", runnable)
	}
	if slices.Contains(runnable, "ghost-check") {
		t.Fatalf("unrunnable check was advertised: %v", runnable)
	}
	if _, leaked := ctxMap["checks"]; leaked {
		t.Fatal("job context leaked check configuration")
	}
}

func TestRunCheckRejectedForPlanJobs(t *testing.T) {
	jr, cap := runCheckFixture(t)
	jr.job.Kind = v1.JobKindPlan
	jr.job.ApprovedPlanRevision = 0
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"unit-check","idempotency_key":"rc-plan"}`); err == nil {
		t.Fatal("plan job invoked platform_run_check")
	}
	if got := len(cap.byKind("test_results")); got != 0 {
		t.Fatalf("plan job emitted check evidence: %d", got)
	}
}

func TestRunCheckFailsClosedForUnknownUnregisteredAndNonRequiredChecks(t *testing.T) {
	jr, cap := runCheckFixture(t)
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "required-but-unregistered")
	jr.d.cfg.Checks["registered-not-required"] = config.CheckConfig{Command: "true"}
	for i, checkID := range []string{"does-not-exist", "required-but-unregistered", "registered-not-required"} {
		args := fmt.Sprintf(`{"check_id":%q,"idempotency_key":"rc-closed-%d"}`, checkID, i)
		if _, err := call(t, jr, "platform_run_check", args); err == nil {
			t.Errorf("check %q was executed", checkID)
		}
	}
	if got := len(cap.byKind("test_results")); got != 0 {
		t.Fatalf("fail-closed checks emitted evidence: %d", got)
	}
}

func TestRunCheckRejectsArbitraryFieldsAndOverrides(t *testing.T) {
	jr, cap := runCheckFixture(t)
	overrides := []string{
		`"command":"/bin/sh"`, `"argv":["-c","id"]`, `"args":["-c","id"]`,
		`"repository":"secondary"`, `"repository_id":"secondary"`, `"path":"/tmp"`,
		`"workdir":"/tmp"`, `"env":{"A":"B"}`, `"environment":{"A":"B"}`,
		`"timeout":1`, `"timeout_seconds":1`, `"network":"none"`, `"network_none":true`,
		`"image":"alpine"`, `"isolation":"direct"`, `"memory_mb":128`, `"cpus":1`,
		`"namespace":"x"`,
	}
	for i, override := range overrides {
		args := fmt.Sprintf(`{"check_id":"unit-check","idempotency_key":"rc-override-%d",%s}`, i, override)
		if _, err := call(t, jr, "platform_run_check", args); err == nil {
			t.Errorf("override %s was accepted", override)
		}
	}
	if got := len(cap.byKind("test_results")); got != 0 {
		t.Fatalf("override attempts emitted evidence: %d", got)
	}
}

func TestRunCheckRepositoryResolutionAndWorktreeEvidence(t *testing.T) {
	jr, cap := runCheckFixture(t)
	primary := jr.repositories[0].WorkDir
	secondary := jr.repositories[1].WorkDir
	head := gitHead(t, primary)

	res, err := call(t, jr, "platform_run_check", `{"check_id":"unit-check","idempotency_key":"rc-clean"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["result"] != "passed" || m["repository_id"] != "primary" {
		t.Fatalf("default primary resolution = %v", m)
	}
	if m["commit_sha"] != head {
		t.Fatalf("commit_sha = %v want %v", m["commit_sha"], head)
	}
	if m["worktree_dirty"] != false {
		t.Fatalf("clean worktree reported dirty: %v", m)
	}

	if err := os.WriteFile(filepath.Join(primary, "marker-primary.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	jr.d.cfg.Checks["default-primary"] = config.CheckConfig{Command: "test", Args: []string{"-f", "marker-primary.txt"}}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "default-primary")
	res, err = call(t, jr, "platform_run_check", `{"check_id":"default-primary","idempotency_key":"rc-default"}`)
	if err != nil {
		t.Fatal(err)
	}
	m = res.(map[string]any)
	if m["result"] != "passed" || m["worktree_dirty"] != true {
		t.Fatalf("uncommitted change was not visible to the check: %v", m)
	}

	if err := os.WriteFile(filepath.Join(secondary, "marker-secondary.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	jr.d.cfg.Checks["secondary-check"] = config.CheckConfig{
		Command: "test", Args: []string{"-f", "marker-secondary.txt"}, RepositoryID: "secondary",
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "secondary-check")
	res, err = call(t, jr, "platform_run_check", `{"check_id":"secondary-check","idempotency_key":"rc-secondary"}`)
	if err != nil {
		t.Fatal(err)
	}
	if m = res.(map[string]any); m["result"] != "passed" || m["repository_id"] != "secondary" {
		t.Fatalf("configured repository was not resolved: %v", m)
	}

	jr.d.cfg.Checks["foreign-check"] = config.CheckConfig{Command: "true", RepositoryID: "outside-job"}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "foreign-check")
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"foreign-check","idempotency_key":"rc-foreign"}`); err == nil {
		t.Fatal("check against a repository outside the job was executed")
	}

	events := cap.byKind("test_results")
	if len(events) != 3 {
		t.Fatalf("events = %d", len(events))
	}
	for _, e := range events {
		if e["phase"] != "iterative" || e["source"] != "runner" {
			t.Fatalf("event provenance = %v", e)
		}
		if e["commit_sha"] == "" || e["commit_sha"] == nil || e["worktree_dirty"] == nil {
			t.Fatalf("event is not bound to HEAD and worktree state: %v", e)
		}
	}
}

func TestRunCheckPassedResultSuppressesOutput(t *testing.T) {
	jr, cap := runCheckFixture(t)
	jr.d.cfg.Checks["chatty"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo all-good; echo detail >&2"},
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "chatty")
	res, err := call(t, jr, "platform_run_check", `{"check_id":"chatty","idempotency_key":"rc-chatty"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["result"] != "passed" {
		t.Fatalf("result = %v", m)
	}
	if _, ok := m["output"]; ok {
		t.Fatalf("passed result returned captured output: %v", m)
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "all-good") || strings.Contains(string(encoded), "detail") {
		t.Fatalf("passed result leaked check output: %s", encoded)
	}
	events := cap.byKind("test_results")
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	for _, key := range []string{"output", "failures", "command", "args", "argv"} {
		if _, ok := events[0][key]; ok {
			t.Fatalf("passed event contains %q: %v", key, events[0])
		}
	}
	eventJSON, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(eventJSON), "all-good") || strings.Contains(string(eventJSON), "detail") {
		t.Fatalf("passed event leaked check output: %s", eventJSON)
	}
}

func TestRunCheckOutputIsBounded(t *testing.T) {
	jr, cap := runCheckFixture(t)
	jr.d.cfg.Checks["loud"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "head -c 65536 /dev/zero | tr '\\000' 'x'; exit 1"},
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "loud")
	res, err := call(t, jr, "platform_run_check", `{"check_id":"loud","idempotency_key":"rc-loud"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["result"] != "failed" {
		t.Fatalf("result = %v", m)
	}
	if m["truncated"] != true {
		t.Fatalf("truncation was not explicit: %v", m)
	}
	output, _ := m["output"].(string)
	if output == "" {
		t.Fatalf("failed result dropped its bounded output: %v", m)
	}
	if len(output) > 8192 {
		t.Fatalf("output was not bounded: %d bytes", len(output))
	}
	events := cap.byKind("test_results")
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	failures, _ := events[0]["failures"].([]string)
	if len(failures) != 1 || len(failures[0]) > 8192 {
		t.Fatalf("event failure output was not bounded: %v", events[0])
	}
}

func TestRunCheckOutputIsRedacted(t *testing.T) {
	jr, cap := runCheckFixture(t)
	secret := "ghp_abcdefghijklmnopqrstuvwxyz012345"
	jr.d.cfg.Checks["leaky"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "printf '%s' 'prefix " + secret + " suffix'; exit 1"},
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "leaky")
	res, err := call(t, jr, "platform_run_check", `{"check_id":"leaky","idempotency_key":"rc-leaky"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["result"] != "failed" {
		t.Fatalf("result = %v", m)
	}
	if _, ok := m["command"]; ok {
		t.Fatalf("tool result disclosed configured command: %v", m)
	}
	if output, _ := m["output"].(string); strings.Contains(output, secret) {
		t.Fatalf("secret leaked in tool result: %q", output)
	}
	events := cap.byKind("test_results")
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	for _, e := range events {
		for _, key := range []string{"command", "args", "argv"} {
			if _, ok := e[key]; ok {
				t.Fatalf("event disclosed administrator-configured %q: %v", key, e)
			}
		}
		if e["suite"] != "leaky" || e["check_id"] != "leaky" {
			t.Fatalf("event identity must be only the opaque check ID: %v", e)
		}
		encoded, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("secret leaked in event: %s", encoded)
		}
	}
}

func TestRunCheckNonZeroExitIsStructuredFailure(t *testing.T) {
	jr, cap := runCheckFixture(t)
	jr.d.cfg.Checks["failing"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo boom; exit 3"},
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "failing")
	res, err := call(t, jr, "platform_run_check", `{"check_id":"failing","idempotency_key":"rc-fail"}`)
	if err != nil {
		t.Fatalf("non-zero exit surfaced as a transport error: %v", err)
	}
	m := res.(map[string]any)
	if m["result"] != "failed" || m["error_classification"] != "exit_nonzero" {
		t.Fatalf("failure result = %v", m)
	}
	if output, _ := m["output"].(string); !strings.Contains(output, "boom") {
		t.Fatalf("failure output missing: %v", m)
	}
	events := cap.byKind("test_results")
	if len(events) != 1 || events[0]["result"] != "failed" {
		t.Fatalf("failure event = %v", events)
	}
}

func TestRunCheckTimeoutIsStructuredError(t *testing.T) {
	jr, cap := runCheckFixture(t)
	jr.d.cfg.Checks["slow"] = config.CheckConfig{Command: "sleep", Args: []string{"30"}, TimeoutSeconds: 1}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "slow")
	start := time.Now()
	res, err := call(t, jr, "platform_run_check", `{"check_id":"slow","idempotency_key":"rc-slow"}`)
	if err != nil {
		t.Fatalf("timeout must be a structured result, not a transport error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("check timeout was not enforced: %v", elapsed)
	}
	m := res.(map[string]any)
	if m["result"] != "error" || m["error_classification"] != "timeout" {
		t.Fatalf("timeout result = %v", m)
	}
	events := cap.byKind("test_results")
	if len(events) != 1 || events[0]["result"] != "error" {
		t.Fatalf("timeout event = %v", events)
	}
}

func TestRunCheckCancellationIsStructuredError(t *testing.T) {
	jr, _ := runCheckFixture(t)
	jr.d.cfg.Checks["slow"] = config.CheckConfig{Command: "sleep", Args: []string{"30"}}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "slow")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	res, err := jr.executeRunCheck(ctx, map[string]any{"check_id": "slow", "idempotency_key": "rc-cancel"})
	if err != nil {
		t.Fatalf("cancellation must be a structured result, not a transport error: %v", err)
	}
	m := res.(map[string]any)
	if m["result"] != "error" || m["error_classification"] != "cancelled" {
		t.Fatalf("cancellation result = %v", m)
	}
}

func TestRunCheckSerializesConcurrentCalls(t *testing.T) {
	jr, _ := runCheckFixture(t)
	dir := jr.repositories[0].WorkDir
	sentinel := filepath.Join(dir, ".runcheck-running")
	script := "if [ -f \"" + sentinel + "\" ]; then exit 9; fi; touch \"" + sentinel + "\"; sleep 1; rm -f \"" + sentinel + "\""
	jr.d.cfg.Checks["serialized"] = config.CheckConfig{Command: "/bin/sh", Args: []string{"-c", script}}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "serialized")
	var wg sync.WaitGroup
	results := make([]any, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = jr.executeRunCheck(context.Background(), map[string]any{
				"check_id": "serialized", "idempotency_key": fmt.Sprintf("rc-serial-%d", i),
			})
		}()
	}
	wg.Wait()
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if results[i].(map[string]any)["result"] != "passed" {
			t.Fatalf("call %d overlapped with another on-demand check: %v", i, results[i])
		}
	}
}

func TestRunCheckStaleLeasePreventsEvidence(t *testing.T) {
	jr, cap := runCheckFixture(t)
	jr.d.leases.Release(jr.job.AttemptID)
	if _, err := jr.executeRunCheck(context.Background(), map[string]any{"check_id": "unit-check", "idempotency_key": "rc-nolease"}); err == nil {
		t.Fatal("check executed without a lease")
	}
	if err := jr.d.leases.Acquire(lease.Lease{
		LeaseID: jr.job.LeaseID, JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, FencingToken: jr.job.FencingToken + 1,
		Kind: jr.job.Kind, ManifestID: jr.job.ManifestID, ManifestDigest: jr.job.ManifestDigest,
		WorkflowRevision: jr.job.WorkflowRevision,
		ExpiresAt:        time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := jr.executeRunCheck(context.Background(), map[string]any{"check_id": "unit-check", "idempotency_key": "rc-stalefence"}); err == nil {
		t.Fatal("check executed with a stale fencing token")
	}
	if got := len(cap.byKind("test_results")); got != 0 {
		t.Fatalf("stale lease emitted authoritative evidence: %d", got)
	}
}

func TestRunCheckLeaseLostDuringExecutionKillsProcessAndEmitsNoEvidence(t *testing.T) {
	jr, cap := runCheckFixture(t)
	done := filepath.Join(jr.repositories[0].WorkDir, ".runcheck-finished")
	jr.d.cfg.Checks["slow"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "sleep 30; touch \"" + done + "\""},
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "slow")
	go func() {
		time.Sleep(300 * time.Millisecond)
		jr.d.leases.Release(jr.job.AttemptID)
	}()
	start := time.Now()
	if _, err := jr.executeRunCheck(context.Background(), map[string]any{"check_id": "slow", "idempotency_key": "rc-midlease"}); err == nil {
		t.Fatal("evidence emitted after the lease was lost")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("check process was not killed promptly after lease loss: %v", elapsed)
	}
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatal("check ran to completion after the lease was lost")
	}
	if got := len(cap.byKind("test_results")); got != 0 {
		t.Fatalf("evidence emitted with a lost lease: %d", got)
	}
}

func TestRunCheckJobCancellationStopsExecutionAndEvidence(t *testing.T) {
	jr, cap := runCheckFixture(t)
	done := filepath.Join(jr.repositories[0].WorkDir, ".runcheck-finished")
	jr.d.cfg.Checks["slow"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "sleep 30; touch \"" + done + "\""},
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "slow")
	go func() {
		time.Sleep(300 * time.Millisecond)
		jr.cancel()
	}()
	start := time.Now()
	if _, err := jr.executeRunCheck(context.Background(), map[string]any{"check_id": "slow", "idempotency_key": "rc-jobcancel"}); err == nil {
		t.Fatal("check returned success after job cancellation")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("check process was not killed promptly after job cancellation: %v", elapsed)
	}
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatal("check ran to completion after job cancellation")
	}
	if got := len(cap.byKind("test_results")); got != 0 {
		t.Fatalf("evidence collected after job cancellation: %d", got)
	}
}

func TestRunCheckIdempotentReplayAcrossRestart(t *testing.T) {
	jr, cap := runCheckFixture(t)
	counter := filepath.Join(jr.repositories[0].WorkDir, ".runcheck-count")
	jr.d.cfg.Checks["counting"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""},
	}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "counting")
	executions := func() int {
		data, err := os.ReadFile(counter)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "run")
	}
	args := `{"check_id":"counting","idempotency_key":"rc-replay"}`
	first, err := call(t, jr, "platform_run_check", args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["result"] != "passed" {
		t.Fatalf("first = %v", first)
	}
	replay, err := call(t, jr, "platform_run_check", args)
	if err != nil {
		t.Fatal(err)
	}
	if replay.(map[string]any)["result"] != "passed" {
		t.Fatalf("replay = %v", replay)
	}
	if executions() != 1 {
		t.Fatalf("idempotent replay re-executed the check: %d", executions())
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("idempotent replay duplicated events: %d", got)
	}
	jr.idempotencyMu.Lock()
	jr.seenIdempotency = map[string]*toolCallMemo{}
	jr.idempotencyMu.Unlock()
	durableReplay, err := call(t, jr, "platform_run_check", args)
	if err != nil {
		t.Fatal(err)
	}
	if durableReplay.(map[string]any)["result"] != "passed" {
		t.Fatalf("durable replay = %v", durableReplay)
	}
	if executions() != 1 {
		t.Fatalf("post-restart replay re-executed the check: %d", executions())
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("post-restart replay duplicated events: %d", got)
	}
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"unit-check","idempotency_key":"rc-replay"}`); err == nil {
		t.Fatal("idempotency key was replayed against a different check")
	}
	if executions() != 1 {
		t.Fatal("cross-check replay executed another check")
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("cross-check replay emitted evidence: %d", got)
	}
}

func TestRunCheckIterativeResultNeverSatisfiesTerminalChecks(t *testing.T) {
	jr, cap := runCheckFixture(t)
	dir := jr.repositories[0].WorkDir
	counter := filepath.Join(dir, ".terminal-count")
	jr.d.cfg.Checks["counting"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""},
	}
	jr.job.RequiredChecks = []string{"counting"}
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"counting","idempotency_key":"rc-terminal"}`); err != nil {
		t.Fatal(err)
	}
	commitSet := map[string]string{"primary": gitHead(t, dir)}
	if !jr.runRequiredChecks(context.Background(), commitSet) {
		t.Fatal("terminal checks failed")
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "run"); got != 2 {
		t.Fatalf("terminal finalization did not re-execute the check: %d executions", got)
	}
	events := cap.byKind("test_results")
	if len(events) != 2 {
		t.Fatalf("events = %d", len(events))
	}
	phases := map[string]int{}
	for _, e := range events {
		phase, _ := e["phase"].(string)
		phases[phase]++
		if e["source"] != "runner" {
			t.Fatalf("event source = %v", e["source"])
		}
		for _, key := range []string{"command", "args", "argv"} {
			if _, ok := e[key]; ok {
				t.Fatalf("event disclosed administrator-configured %q: %v", key, e)
			}
		}
	}
	if phases["iterative"] != 1 || phases["terminal"] != 1 {
		t.Fatalf("event phases = %v", phases)
	}
}

func reopenRunCheckJournal(t *testing.T, jr *jobRunner) {
	t.Helper()
	if err := jr.d.journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := journal.Open(filepath.Join(jr.d.cfg.StateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	jr.d.journal = reopened
}

func resetRunCheckMemo(jr *jobRunner) {
	jr.idempotencyMu.Lock()
	jr.seenIdempotency = map[string]*toolCallMemo{}
	jr.idempotencyMu.Unlock()
}

func TestRunCheckConcurrentCrossCheckKeyClaimConverges(t *testing.T) {
	jr, cap := runCheckFixture(t)
	counter := filepath.Join(jr.repositories[0].WorkDir, ".claim-count")
	script := "echo run >> \"" + counter + "\""
	jr.d.cfg.Checks["unit-check"] = config.CheckConfig{Command: "/bin/sh", Args: []string{"-c", script}}
	jr.d.cfg.Checks["check-b"] = config.CheckConfig{Command: "/bin/sh", Args: []string{"-c", script}}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "check-b")
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, checkID := range []string{"unit-check", "check-b"} {
		wg.Add(1)
		go func(i int, checkID string) {
			defer wg.Done()
			<-barrier
			_, errs[i] = jr.executeRunCheck(context.Background(), map[string]any{
				"check_id": checkID, "idempotency_key": "rc-shared",
			})
		}(i, checkID)
	}
	close(barrier)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one cross-check claim may execute: %v", errs)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "run"); got != 1 {
		t.Fatalf("executions = %d, want exactly 1", got)
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("events = %d, want exactly 1", got)
	}
}

func TestRunCheckClaimBindingSurvivesRestart(t *testing.T) {
	jr, cap := runCheckFixture(t)
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"unit-check","idempotency_key":"rc-bind"}`); err != nil {
		t.Fatal(err)
	}
	jr.d.cfg.Checks["check-b"] = config.CheckConfig{Command: "true"}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "check-b")
	reopenRunCheckJournal(t, jr)
	resetRunCheckMemo(jr)
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"check-b","idempotency_key":"rc-bind"}`); err == nil {
		t.Fatal("idempotency key was rebound to a different check after restart")
	}
	replay, err := call(t, jr, "platform_run_check", `{"check_id":"unit-check","idempotency_key":"rc-bind"}`)
	if err != nil {
		t.Fatalf("same-check replay after restart: %v", err)
	}
	if replay.(map[string]any)["result"] != "passed" {
		t.Fatalf("replay = %v", replay)
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("restart replay duplicated evidence: %d", got)
	}
}

func TestPlanJobContextExposesNoRunnableChecks(t *testing.T) {
	jr, _ := runCheckFixture(t)
	jr.job.Kind = v1.JobKindPlan
	jr.job.ApprovedPlanRevision = 0
	res, err := call(t, jr, "platform_get_job_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	raw, advertised := m["runnable_checks"]
	if !advertised {
		return
	}
	checks, _ := raw.([]string)
	if len(checks) != 0 {
		t.Fatalf("plan job advertised runnable checks from stale input: %v", checks)
	}
}
