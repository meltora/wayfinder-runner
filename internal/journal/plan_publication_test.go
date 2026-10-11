package journal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func testPlanPublicationClaim(attemptID, eventID string) PlanPublicationClaim {
	return PlanPublicationClaim{
		AttemptID: attemptID, Tool: "platform_publish_plan", IdempotencyKey: "plan-" + attemptID,
		EventID: eventID, InputDigest: strings.Repeat("b", 64), Payload: []byte(`{"plan":"x"}`),
	}
}

func TestPlanPublicationQueuedRequiresClaimAndDurableEvent(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()

	queued, err := j.PlanPublicationQueued(ctx, "attempt-none")
	if err != nil || queued {
		t.Fatalf("no claim: queued=%v err=%v", queued, err)
	}

	if _, _, err := j.ClaimPlanPublication(ctx, testPlanPublicationClaim("attempt-claim", "evt-missing")); err != nil {
		t.Fatal(err)
	}
	queued, err = j.PlanPublicationQueued(ctx, "attempt-claim")
	if err != nil || queued {
		t.Fatalf("claim without event: queued=%v err=%v", queued, err)
	}

	if _, _, err := j.ClaimPlanPublication(ctx, testPlanPublicationClaim("attempt-wrong-kind", "cmd-shared-id")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(ctx, Record{ID: "cmd-shared-id", Kind: KindCommand, Payload: []byte("c"), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	queued, err = j.PlanPublicationQueued(ctx, "attempt-wrong-kind")
	if err != nil || queued {
		t.Fatalf("claim bound to non-event record: queued=%v err=%v", queued, err)
	}

	if _, _, err := j.ClaimPlanPublication(ctx, testPlanPublicationClaim("attempt-ok", "evt-ok")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(ctx, Record{ID: "evt-ok", Kind: KindEvent, Payload: []byte(`{"kind":"plan_published"}`), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	queued, err = j.PlanPublicationQueued(ctx, "attempt-ok")
	if err != nil || !queued {
		t.Fatalf("claim with event: queued=%v err=%v", queued, err)
	}
	if err := j.Ack(ctx, "evt-ok"); err != nil {
		t.Fatal(err)
	}
	queued, err = j.PlanPublicationQueued(ctx, "attempt-ok")
	if err != nil || !queued {
		t.Fatalf("acknowledged event no longer counted: queued=%v err=%v", queued, err)
	}
}

func TestPlanPublicationQueuedReportsJournalErrors(t *testing.T) {
	j := openTest(t)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.PlanPublicationQueued(context.Background(), "attempt-1"); err == nil {
		t.Fatal("closed journal did not report an error")
	}
}

func TestClaimPlanPublicationRejectsInvalidClaims(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*PlanPublicationClaim){
		"empty attempt":     func(c *PlanPublicationClaim) { c.AttemptID = "" },
		"bad digest":        func(c *PlanPublicationClaim) { c.InputDigest = "not-a-digest" },
		"empty payload":     func(c *PlanPublicationClaim) { c.Payload = nil },
		"oversized payload": func(c *PlanPublicationClaim) { c.Payload = make([]byte, maxPlanPublicationPayloadBytes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			claim := testPlanPublicationClaim("attempt-invalid", "evt-invalid")
			mutate(&claim)
			if _, created, err := j.ClaimPlanPublication(ctx, claim); err == nil || created {
				t.Fatalf("created=%v err=%v", created, err)
			}
		})
	}
	queued, err := j.PlanPublicationQueued(ctx, "attempt-invalid")
	if err != nil || queued {
		t.Fatalf("invalid claim persisted: queued=%v err=%v", queued, err)
	}
}

func TestClaimPlanPublicationReturnsExistingClaimAndRejectsCompetingClaim(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	claim := testPlanPublicationClaim("attempt-once", "evt-once")
	if _, created, err := j.ClaimPlanPublication(ctx, claim); err != nil || !created {
		t.Fatalf("first claim created=%v err=%v", created, err)
	}
	existing, created, err := j.ClaimPlanPublication(ctx, claim)
	if err != nil || created || existing.EventID != "evt-once" {
		t.Fatalf("retried claim existing=%+v created=%v err=%v", existing, created, err)
	}
	competing := testPlanPublicationClaim("attempt-once", "evt-other")
	competing.IdempotencyKey = "plan-other"
	if _, created, err := j.ClaimPlanPublication(ctx, competing); err != ErrPlanPublicationConflict || created {
		t.Fatalf("competing claim created=%v err=%v", created, err)
	}
}
