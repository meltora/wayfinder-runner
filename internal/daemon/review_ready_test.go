package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/journal"
)

func TestMarkReviewReadyRecordsOneLocalIntentWithoutPublishingEvidence(t *testing.T) {
	jr, captured := testJobRunner(t)
	jr.job.PushBranch = true

	result, err := call(t, jr, "platform_mark_review_ready", `{
		"summary":"Feature implementation and final checks are ready for review",
		"known_limitations":["Email delivery is disabled"],
		"idempotency_key":"review-ready"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	response := result.(map[string]any)
	if response["ok"] != true || response["status"] != "pending_runner_finalization" {
		t.Fatalf("result = %v", response)
	}
	if events := captured.byKind("review_ready"); len(events) != 0 {
		t.Fatalf("agent call published review authority early: %v", events)
	}
	intent, ok := jr.reviewReadyIntentSnapshot()
	if !ok || intent.summary != "Feature implementation and final checks are ready for review" ||
		len(intent.limitations) != 1 || intent.idempotencyKey != "review-ready" {
		t.Fatalf("intent = %#v, ok = %v", intent, ok)
	}

	if _, err := call(t, jr, "platform_mark_review_ready", `{
		"summary":"A changed review request",
		"idempotency_key":"second-review-ready"
	}`); err == nil || !strings.Contains(err.Error(), "already recorded") {
		t.Fatalf("second intent error = %v", err)
	}
	if events := captured.byKind("review_ready"); len(events) != 0 {
		t.Fatalf("conflicting intent published evidence: %v", events)
	}
}

func TestMarkReviewReadyRejectsUnsafeOrUnboundedArguments(t *testing.T) {
	tooMany := make([]string, 129)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("limitation-%d", index)
	}
	cases := []string{
		`{"summary":"","idempotency_key":"empty"}`,
		`{"summary":"   ","idempotency_key":"blank"}`,
		`{"summary":"unsafe\u0001text","idempotency_key":"control"}`,
		fmt.Sprintf(`{"summary":%q,"idempotency_key":"long"}`, strings.Repeat("x", 16*1024+1)),
		`{"summary":"ready","known_limitations":"none","idempotency_key":"limitations-type"}`,
		`{"summary":"ready","known_limitations":["ok",7],"idempotency_key":"limitation-type"}`,
		`{"summary":"ready","known_limitations":["   "],"idempotency_key":"limitation-blank"}`,
		`{"summary":"ready","known_limitations":["unsafe\u0001text"],"idempotency_key":"limitation-control"}`,
		fmt.Sprintf(`{"summary":"ready","known_limitations":[%q],"idempotency_key":"limitation-long"}`, strings.Repeat("x", 4097)),
		fmt.Sprintf(`{"summary":"ready","known_limitations":%s,"idempotency_key":"limitations-many"}`, mustJSON(t, tooMany)),
		`{"summary":"ready","evidence":["self-claimed"],"idempotency_key":"unknown"}`,
		`{"summary":"ready","commit_set":{"repo":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"idempotency_key":"commits"}`,
		`{"summary":"ready","approved":true,"idempotency_key":"approval"}`,
	}
	for index, args := range cases {
		jr, captured := testJobRunner(t)
		jr.job.PushBranch = true
		if _, err := call(t, jr, "platform_mark_review_ready", args); err == nil {
			t.Fatalf("case %d accepted: %s", index, args)
		}
		if _, ok := jr.reviewReadyIntentSnapshot(); ok {
			t.Fatalf("case %d retained an invalid intent", index)
		}
		if events := captured.byKind("review_ready"); len(events) != 0 {
			t.Fatalf("case %d published evidence: %v", index, events)
		}
	}
}

func TestMarkReviewReadyRequiresSignedBranchPublicationAuthority(t *testing.T) {
	jr, captured := testJobRunner(t)
	jr.job.PushBranch = false
	if _, err := call(t, jr, "platform_mark_review_ready", `{
		"summary":"ready",
		"idempotency_key":"no-push"
	}`); err == nil || !strings.Contains(err.Error(), "branch publication") {
		t.Fatalf("error = %v", err)
	}
	if events := captured.byKind("review_ready"); len(events) != 0 {
		t.Fatalf("non-push job published review evidence: %v", events)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFinalReviewReadySerializesMissingLimitationsAsEmptyArray(t *testing.T) {
	jr, captured, want := previewFixture(t)
	jr.job.PushBranch = true
	if _, err := call(t, jr, "platform_mark_review_ready", `{
		"summary":"Ready for review",
		"idempotency_key":"review-ready-no-limitations"
	}`); err != nil {
		t.Fatal(err)
	}
	if err := jr.publishFinalReviewReady(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	events := captured.byKind("review_ready")
	if len(events) != 1 {
		t.Fatalf("review_ready events = %v", events)
	}
	raw, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"known_limitations":[]`) {
		t.Fatalf("review_ready payload = %s", raw)
	}
	records, err := jr.d.journal.Unacked(context.Background(), journal.KindEvent, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if strings.Contains(string(record.Payload), `"kind":"review_ready"`) {
			found = true
			if !strings.Contains(string(record.Payload), `"known_limitations":[]`) {
				t.Fatalf("journaled review_ready = %s", record.Payload)
			}
		}
	}
	if !found {
		t.Fatal("review_ready was not journaled")
	}
}

func TestReviewReadyIntentSnapshotKeepsEmptyLimitationsNonNil(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.PushBranch = true
	if _, err := call(t, jr, "platform_mark_review_ready", `{
		"summary":"Ready for review",
		"known_limitations":[],
		"idempotency_key":"review-ready-empty-limitations"
	}`); err != nil {
		t.Fatal(err)
	}
	intent, ok := jr.reviewReadyIntentSnapshot()
	if !ok || intent.limitations == nil || len(intent.limitations) != 0 {
		t.Fatalf("intent limitations = %#v, ok = %v", intent.limitations, ok)
	}
}
