package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/hookspool"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/mcpbridge"
)

type capturedEvents struct {
	mu     sync.Mutex
	events []map[string]any
}

func (c *capturedEvents) sink(ctx context.Context, payload any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := payload.(map[string]any); ok {
		c.events = append(c.events, m)
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	c.events = append(c.events, event)
	return nil
}

func (c *capturedEvents) byKind(kind string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, e := range c.events {
		if e["kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func testJobRunner(t *testing.T) (*jobRunner, *capturedEvents) {
	t.Helper()
	stateDir := t.TempDir()
	cap := &capturedEvents{}
	d := &Daemon{
		cfg:        config.Config{StateDir: stateDir},
		logger:     slog.Default(),
		leases:     lease.NewManager(),
		collectors: collector.NewStore(stateDir),
		uploader:   newArtifactUploader("http://127.0.0.1:1"),
		eventSink:  cap.sink,
	}
	var err error
	d.journal, err = journal.Open(filepath.Join(stateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.journal.Close() })
	job := v1.JobSpec{
		JobID: "j1", AttemptID: "a1", FencingToken: 7, Kind: v1.JobKindBuild,
		LeaseID: "lease-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		WorkflowRevision: 2, ApprovedPlanRevision: 1,
	}
	if err := d.leases.Acquire(lease.Lease{
		LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID, FencingToken: job.FencingToken,
		Kind: job.Kind, ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest, WorkflowRevision: job.WorkflowRevision,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	jr := &jobRunner{
		d: d, job: job, workDir: t.TempDir(), logger: slog.Default(),
		decisions:       map[string]chan v1.CommandPayload{},
		seenIdempotency: map[string]*toolCallMemo{},
	}
	jr.ctx, jr.cancel = context.WithCancel(context.Background())
	t.Cleanup(jr.cancel)
	return jr, cap
}

func TestInternalHookEventOnlySpoolsAllowlistedMetadata(t *testing.T) {
	jr, _ := testJobRunner(t)
	_, err := call(t, jr, mcpbridge.InternalHookEventTool, `{"event_type":"post-tool","idempotency_key":"hook-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	spool, err := hookspool.NewSpool(filepath.Join(jr.d.cfg.StateDir, "hook-spool"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := spool.Drain(10)
	if err != nil || len(events) != 1 {
		t.Fatalf("hook events = %v, %v", events, err)
	}
	if len(events[0].Data) != 0 {
		t.Fatalf("hook telemetry included arbitrary data: %v", events[0].Data)
	}
	if _, err := call(t, jr, mcpbridge.InternalHookEventTool, `{"event_type":"post-tool","data":{"source":"repository contents"},"idempotency_key":"hook-2"}`); err == nil {
		t.Fatal("hook telemetry accepted arbitrary data")
	}
	if _, err := call(t, jr, mcpbridge.InternalHookEventTool, `{"event_type":"repository-contents","idempotency_key":"hook-3"}`); err == nil {
		t.Fatal("hook telemetry accepted an unregistered event type")
	}
}

func TestPlanJobRejectsBuildOnlyTools(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	jr.job.ApprovedPlanRevision = 0
	buildOnly := []string{
		"platform_publish_test_results",
		"platform_publish_manual_test_plan",
		"platform_publish_preview",
		"platform_publish_artifact",
		"platform_mark_review_ready",
	}
	for i, tool := range buildOnly {
		args := fmt.Sprintf(`{"idempotency_key":"build-only-%d"}`, i)
		if _, err := call(t, jr, tool, args); err == nil {
			t.Errorf("plan job was allowed to call %s", tool)
		}
	}
	contextResult, err := call(t, jr, "platform_get_job_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	capabilities, ok := contextResult.(map[string]any)["capabilities"].([]string)
	if !ok {
		t.Fatalf("capabilities have unexpected type: %T", contextResult.(map[string]any)["capabilities"])
	}
	for _, capability := range capabilities {
		for _, forbidden := range buildOnly {
			if capability == forbidden {
				t.Fatalf("plan context advertised build-only capability %q", capability)
			}
		}
	}
}

func call(t *testing.T, jr *jobRunner, tool string, args string) (any, error) {
	t.Helper()
	return jr.handleToolCall(context.Background(), tool, json.RawMessage(args))
}

func TestToolCallLeaseValidation(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.leases.Release("a1")
	if _, err := call(t, jr, "platform_report_progress", `{"milestone":"x"}`); err == nil {
		t.Fatal("expected lease validation error")
	}
}

func TestPlanAndRevisionFlow(t *testing.T) {
	jr, cap := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	args := planPublicationArgs(t, "plan-1", "Initial reviewed plan", "Initial behavior", "")
	res, err := call(t, jr, "platform_publish_plan", args)
	if err != nil {
		t.Fatalf("publish plan: %v", err)
	}
	eventID, _ := res.(map[string]any)["event_id"].(string)
	if res.(map[string]any)["published"] != true || eventID == "" {
		t.Fatalf("publication result = %v", res)
	}
	duplicate, err := call(t, jr, "platform_publish_plan", args)
	if err != nil || duplicate.(map[string]any)["event_id"] != eventID {
		t.Fatalf("in-memory retry did not recover plan result: %v, %v", duplicate, err)
	}
	jr.idempotencyMu.Lock()
	jr.seenIdempotency = map[string]*toolCallMemo{}
	jr.idempotencyMu.Unlock()
	durableDuplicate, err := call(t, jr, "platform_publish_plan", args)
	if err != nil || durableDuplicate.(map[string]any)["event_id"] != eventID {
		t.Fatalf("durable retry did not recover plan result: %v, %v", durableDuplicate, err)
	}
	if _, err := call(t, jr, "platform_publish_plan", planPublicationArgs(t, "plan-2", "replacement", "Changed", "")); err == nil {
		t.Fatal("a second publication under a different key was accepted")
	}
	jr.idempotencyMu.Lock()
	jr.seenIdempotency = map[string]*toolCallMemo{}
	jr.idempotencyMu.Unlock()
	if _, err := call(t, jr, "platform_publish_plan", planPublicationArgs(t, "plan-1", "mutated replay", "Changed", "")); err == nil {
		t.Fatal("a durable same-key replay with changed input was accepted")
	}
	if _, err := call(t, jr, "platform_publish_plan_revision", planPublicationArgs(t, "revision-invalid", "revision", "Changed", "revision-1")); err == nil {
		t.Fatal("revision publication without immutable revision context was accepted")
	}
	plans := cap.byKind("plan_published")
	if len(plans) != 1 {
		t.Fatalf("plan events = %d", len(plans))
	}
	plan, ok := plans[0]["plan"].(v1.PlanContent)
	if !ok || plan.Behavior != "Initial behavior" {
		t.Fatalf("strict plan content = %#v", plans[0]["plan"])
	}
	for _, legacy := range []string{"summary", "approach", "revision", "base_revision"} {
		if _, exists := plans[0][legacy]; exists {
			t.Fatalf("legacy field %s survived in event: %v", legacy, plans[0])
		}
	}
	if plans[0]["manifest_kind"] != "plan" || plans[0]["base_revision_id"] != nil || plans[0]["change_summary"] != "Initial reviewed plan" {
		t.Fatalf("initial event authority = %v", plans[0])
	}
}

func TestPlanRevisionMustUseReviewedBaseRevisionIdentity(t *testing.T) {
	jr, cap := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	jr.job.PlanRevisionContext = validDaemonRevisionContext()

	if _, err := call(t, jr, "platform_publish_plan", planPublicationArgs(t, "initial-invalid", "initial", "Changed", "")); err == nil {
		t.Fatal("initial publication was accepted for a revision manifest")
	}
	if _, err := call(t, jr, "platform_publish_plan_revision", planPublicationArgs(t, "wrong-base", "addressed feedback", "Changed", "revision-other")); err == nil {
		t.Fatal("revision publication accepted a stale base revision ID")
	}
	result, err := call(t, jr, "platform_publish_plan_revision", planPublicationArgs(t, "revision-1", "Addressed rollback feedback", "Changed behavior", "revision-1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["published"] != true {
		t.Fatalf("revision result = %v", result)
	}
	events := cap.byKind("plan_published")
	if len(events) != 1 || events[0]["base_revision_id"] != "revision-1" || events[0]["manifest_kind"] != "plan" {
		t.Fatalf("revision event = %v", events)
	}
}

func TestBuildJobCannotPublishPlanningAuthority(t *testing.T) {
	jr, _ := testJobRunner(t)
	if _, err := call(t, jr, "platform_publish_plan", planPublicationArgs(t, "build-plan", "invalid", "Behavior", "")); err == nil {
		t.Fatal("build job published a plan event")
	}
}

func TestPendingPlanPublicationRecoversAcrossEveryCrashWindow(t *testing.T) {
	for _, publishBeforeCrash := range []bool{false, true} {
		t.Run(fmt.Sprintf("event_published_%v", publishBeforeCrash), func(t *testing.T) {
			jr, captured := testJobRunner(t)
			jr.job.Kind = v1.JobKindPlan
			args := planPublicationArgs(t, "plan-crash", "Crash-safe plan", "Recovered behavior", "")
			raw := map[string]any{}
			if err := json.Unmarshal([]byte(args), &raw); err != nil {
				t.Fatal(err)
			}
			claim, err := jr.preparePlanPublicationClaim(publishPlanTool, "plan-crash", raw)
			if err != nil {
				t.Fatal(err)
			}
			stored, _, err := jr.d.journal.ClaimPlanPublication(context.Background(), claim)
			if err != nil {
				t.Fatal(err)
			}
			if state, _, err := jr.d.journal.BeginToolCall(context.Background(), jr.job.AttemptID, publishPlanTool, "plan-crash"); err != nil || state != journal.ToolCallStarted {
				t.Fatalf("stage pending tool call = %s, %v", state, err)
			}
			if publishBeforeCrash {
				event, err := jr.validatePlanPublicationPayload(stored.Payload, publishPlanTool)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := jr.d.sendEventWithStableID(context.Background(), stored.EventID, event.asMap()); err != nil {
					t.Fatal(err)
				}
			}

			result, err := call(t, jr, publishPlanTool, args)
			if err != nil || result.(map[string]any)["event_id"] != stored.EventID {
				t.Fatalf("recovered result = %v, err=%v", result, err)
			}
			if events := captured.byKind("plan_published"); len(events) != 1 {
				t.Fatalf("logical plan events = %d, want 1", len(events))
			}
		})
	}
}

func TestPlanPublicationFailsClosedOnStableEventRecordCollision(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	key := "plan-collision"
	eventID := planPublicationEventID(jr.job.AttemptID, jr.job.ManifestDigest, publishPlanTool, key)
	if err := jr.d.journal.Append(context.Background(), journal.Record{
		ID: eventID, Kind: journal.KindEvent, Payload: []byte(`{}`), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, jr, publishPlanTool, planPublicationArgs(t, key, "Plan", "Behavior", "")); err == nil {
		t.Fatal("colliding durable event was trusted as plan evidence")
	}
}

func TestPlanPublicationPreservesFencingTokenAboveFloatPrecision(t *testing.T) {
	jr, captured := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	jr.job.FencingToken = (1 << 53) + 1
	jr.d.leases.Release(jr.job.AttemptID)
	if err := jr.d.leases.Acquire(lease.Lease{
		LeaseID: jr.job.LeaseID, JobID: jr.job.JobID, AttemptID: jr.job.AttemptID,
		Kind: jr.job.Kind, ManifestID: jr.job.ManifestID, ManifestDigest: jr.job.ManifestDigest,
		WorkflowRevision: jr.job.WorkflowRevision, FencingToken: jr.job.FencingToken,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, jr, publishPlanTool, planPublicationArgs(t, "plan-exact-fence", "Exact fence", "Behavior", "")); err != nil {
		t.Fatal(err)
	}
	events := captured.byKind("plan_published")
	if len(events) != 1 || events[0]["fencing_token"] != jr.job.FencingToken {
		t.Fatalf("plan event lost fencing precision: %v", events)
	}
}

func planPublicationArgs(t *testing.T, key, changeSummary, behavior, baseRevisionID string) string {
	t.Helper()
	plan := validDaemonRevisionContext().BasePlan.Content
	plan.Behavior = behavior
	args := map[string]any{
		"plan": plan, "change_summary": changeSummary, "idempotency_key": key,
	}
	if baseRevisionID != "" {
		args["base_revision_id"] = baseRevisionID
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestIdempotencyKeyDedupes(t *testing.T) {
	jr, cap := testJobRunner(t)
	args := `{"milestone":"m","idempotency_key":"k1"}`
	if _, err := call(t, jr, "platform_report_progress", args); err != nil {
		t.Fatal(err)
	}
	res, err := call(t, jr, "platform_report_progress", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["ok"] != true {
		t.Fatalf("duplicate did not return the original result: %v", res)
	}
	if len(cap.byKind("progress")) != 1 {
		t.Fatalf("progress events = %d", len(cap.byKind("progress")))
	}
	jr.idempotencyMu.Lock()
	jr.seenIdempotency = map[string]*toolCallMemo{}
	jr.idempotencyMu.Unlock()
	res, err = call(t, jr, "platform_report_progress", args)
	if err != nil || res.(map[string]any)["ok"] != true {
		t.Fatalf("durable duplicate = %v, %v", res, err)
	}
	if len(cap.byKind("progress")) != 1 {
		t.Fatalf("durable retry emitted another event: %d", len(cap.byKind("progress")))
	}
}

func TestFailedToolCallCanRetrySameIdempotencyKey(t *testing.T) {
	jr, cap := testJobRunner(t)
	if _, err := call(t, jr, "platform_publish_test_results", `{"result":"bogus","idempotency_key":"tests-1"}`); err == nil {
		t.Fatal("invalid result unexpectedly succeeded")
	}
	if _, err := call(t, jr, "platform_publish_test_results", `{"suite":"unit","result":"passed","idempotency_key":"tests-1"}`); err != nil {
		t.Fatalf("corrected retry failed: %v", err)
	}
	if len(cap.byKind("test_results")) != 1 {
		t.Fatalf("test result events = %d", len(cap.byKind("test_results")))
	}
}

func TestArtifactPathContainment(t *testing.T) {
	jr, _ := testJobRunner(t)
	if _, err := call(t, jr, "platform_publish_artifact", `{"name":"x","path":"../../etc/passwd","idempotency_key":"artifact-escape"}`); err == nil {
		t.Fatal("path escape must be rejected")
	}
}

func TestArtifactSymlinkCannotEscapeWorkspace(t *testing.T) {
	jr, _ := testJobRunner(t)
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "runner-key.pem")
	if err := os.WriteFile(secretPath, []byte("runner private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(jr.workDir, "leak.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, jr, "platform_publish_artifact", `{"name":"leak.pem","path":"leak.pem","idempotency_key":"artifact-symlink"}`); err == nil {
		t.Fatal("artifact publication followed a symlink outside the workspace")
	}
	artifactDir := filepath.Join(jr.d.collectors.Root, "artifacts", "j1", "a1")
	if entries, err := os.ReadDir(artifactDir); err == nil && len(entries) != 0 {
		t.Fatalf("artifact was stored before the symlink was rejected: %v", entries)
	}
}

func TestArtifactPublishRegisters(t *testing.T) {
	jr, cap := testJobRunner(t)
	if err := os.WriteFile(filepath.Join(jr.workDir, "plan.md"), []byte("# Plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := call(t, jr, "platform_publish_artifact", `{"name":"plan.md","path":"plan.md","content_type":"text/markdown","idempotency_key":"artifact-plan"}`)
	if err != nil {
		t.Fatalf("publish artifact: %v", err)
	}
	if result.(map[string]any)["queued"] != true {
		t.Fatalf("artifact result = %v", result)
	}
	if got := len(cap.byKind("artifact_registered")); got != 0 {
		t.Fatalf("artifact was registered before upload: %d", got)
	}
	entries, readErr := os.ReadDir(filepath.Join(jr.d.collectors.Root, "artifacts", "j1", "a1"))
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("queued artifact not retained: entries=%v err=%v", entries, readErr)
	}
}

func TestReviewReadyAndTestResults(t *testing.T) {
	jr, cap := testJobRunner(t)
	if _, err := call(t, jr, "platform_publish_test_results", `{"suite":"unit","command":"go test","result":"bogus","idempotency_key":"tests-bogus"}`); err == nil {
		t.Fatal("invalid result must be rejected")
	}
	if _, err := call(t, jr, "platform_publish_test_results", `{"suite":"unit","command":"go test","result":"passed","duration_ms":123,"idempotency_key":"tests-pass"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, jr, "platform_mark_review_ready", `{"summary":"done","idempotency_key":"review-ready"}`); err != nil {
		t.Fatal(err)
	}
	if len(cap.byKind("test_results")) != 1 || len(cap.byKind("review_ready")) != 1 {
		t.Fatalf("events = %v", cap.events)
	}
}

func TestHumanInputAndBlocker(t *testing.T) {
	jr, cap := testJobRunner(t)
	if _, err := call(t, jr, "platform_request_human_input", `{"question":"Which DB?","options":["pg","mysql"],"timeout_seconds":1,"idempotency_key":"decision-timeout"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, jr, "platform_report_blocker", `{"blocker_type":"missing_secret","can_continue":true,"idempotency_key":"blocker-1"}`); err != nil {
		t.Fatal(err)
	}
	if len(cap.byKind("decision_request")) != 1 || len(cap.byKind("blocker")) != 1 {
		t.Fatalf("events = %v", cap.events)
	}
}

func TestRemainingAuthoritativeToolEvents(t *testing.T) {
	jr, cap := testJobRunner(t)
	calls := []struct {
		tool string
		args string
	}{
		{"platform_report_failure", `{"failure_type":"build","message":"compiler failed","retryable":true,"idempotency_key":"failure-1"}`},
		{"platform_publish_decision", `{"decision":"use postgres","rationale":"existing stack","evidence":["ADR-1"],"idempotency_key":"decision-1"}`},
		{"platform_publish_manual_test_plan", `{"steps":["open preview","submit form"],"expected_results":["success"],"cleanup":"none","idempotency_key":"manual-1"}`},
		{"platform_publish_preview", `{"url":"https://preview.example.test/feature","health":"healthy","commit_sha":"abc","idempotency_key":"preview-1"}`},
	}
	for _, tc := range calls {
		if _, err := call(t, jr, tc.tool, tc.args); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
	}
	for _, kind := range []string{"job_failure", "decision", "manual_test_plan", "preview"} {
		if len(cap.byKind(kind)) != 1 {
			t.Fatalf("missing %s event: %v", kind, cap.events)
		}
	}
}

func TestToolBoundaryRejectsMalformedAndResourceExhaustionInputs(t *testing.T) {
	jr, _ := testJobRunner(t)
	if _, err := jr.handleToolCall(context.Background(), "platform_report_progress", json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := call(t, jr, "platform_report_progress", `{"milestone":"x"}`); err == nil {
		t.Fatal("missing idempotency key accepted")
	}
	if _, err := call(t, jr, "platform_report_progress", `{"milestone":"x","idempotency_key":"bad key"}`); err == nil {
		t.Fatal("unsafe idempotency key accepted")
	}
	jr.d.paused.Store(true)
	if _, err := call(t, jr, "platform_get_job_context", `{}`); err != nil {
		t.Fatalf("paused runner interrupted an active job tool call: %v", err)
	}
	jr.d.paused.Store(false)

	tooMany := map[string]any{}
	for i := 0; i < 65; i++ {
		tooMany[fmt.Sprintf("field-%d", i)] = true
	}
	if err := validateToolArguments(tooMany, 0); err == nil {
		t.Fatal("too many fields accepted")
	}
	if err := validateToolArguments(map[string]any{strings.Repeat("x", 129): true}, 0); err == nil {
		t.Fatal("oversized field name accepted")
	}
	if err := validateToolArguments([]any{make([]any, 257)}, 0); err == nil {
		t.Fatal("oversized array accepted")
	}
	if err := validateToolArguments(strings.Repeat("x", (128<<10)+1), 0); err == nil {
		t.Fatal("oversized string accepted")
	}
	deep := any("leaf")
	for range 7 {
		deep = map[string]any{"nested": deep}
	}
	if err := validateToolArguments(deep, 0); err == nil {
		t.Fatal("deep nesting accepted")
	}

	jr.idempotencyMu.Lock()
	jr.seenIdempotency = make(map[string]*toolCallMemo, 10_000)
	for i := 0; i < 10_000; i++ {
		jr.seenIdempotency[fmt.Sprintf("scope-%d", i)] = &toolCallMemo{done: make(chan struct{})}
	}
	jr.idempotencyMu.Unlock()
	if _, err := call(t, jr, "platform_report_progress", `{"milestone":"x","idempotency_key":"limit"}`); err == nil {
		t.Fatal("idempotency resource limit not enforced")
	}
}

func TestToolSpecificValidation(t *testing.T) {
	jr, _ := testJobRunner(t)
	cases := []struct {
		tool string
		args string
	}{
		{"platform_report_failure", `{"failure_type":"x","message":"","idempotency_key":"a"}`},
		{"platform_publish_plan_revision", `{"base_revision_id":"revision-1","change_summary":"x","plan":{},"idempotency_key":"b"}`},
		{"platform_publish_test_results", `{"suite":"unit","result":"passed","duration_ms":-1,"idempotency_key":"c"}`},
		{"platform_publish_preview", `{"url":"http://preview.example.test","health":"healthy","idempotency_key":"d"}`},
		{"platform_publish_preview", `{"url":"https://preview.example.test","health":"unknown","idempotency_key":"e"}`},
		{"platform_mark_review_ready", `{"summary":"","idempotency_key":"f"}`},
		{"platform_publish_artifact", `{"name":"x","path":"missing","content_type":"not a media type","idempotency_key":"g"}`},
		{"unknown_tool", `{"idempotency_key":"h"}`},
	}
	for _, tc := range cases {
		if _, err := call(t, jr, tc.tool, tc.args); err == nil {
			t.Fatalf("%s accepted invalid args %s", tc.tool, tc.args)
		}
	}
}
