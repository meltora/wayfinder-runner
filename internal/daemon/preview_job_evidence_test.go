package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/repo"
)

const previewEvidenceRedeployEdgeHost = "zyxwvutsrqponmlkjihgfedcba.previews.example.test"

func previewEvidenceFixture(t *testing.T) (*jobRunner, *capturedEvents, map[string]string) {
	t.Helper()
	jr, captured, want := deployPreviewFixture(t)
	for index := range jr.repositories {
		jr.repositories[index].Branch = "feature/evidence"
	}
	jr.reviewReadyIntent = &reviewReadyIntent{
		summary: "Ready for review", limitations: []string{}, idempotencyKey: "review-ready-evidence",
	}
	return jr, captured, want
}

func deployPreviewResultAt(jr *jobRunner, event map[string]any, edgeHost string, expiresAt time.Time) {
	digest, _ := event["commit_set_digest"].(string)
	result := v1.PreviewDeployResultEvent{Outcome: deployedPreviewOutcome()}
	result.IdempotencyKey = v1.PreviewJobDeployIdempotencyKey(previewDeployRequestedID(event))
	result.PreviewID = previewDeployToolPreviewID
	result.ExecutionManifestID = jr.job.ManifestID
	result.CommitSetDigest = digest
	result.EdgeHost = edgeHost
	result.ExpiresAt = expiresAt
	jr.d.previewJobDeployResultSent(result)
}

func finalizeEvidenceBuild(t *testing.T, jr *jobRunner, publishReviewReady bool) string {
	t.Helper()
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	state, _, err := jr.finalizeBuild(context.Background(), rm, publishReviewReady)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func makePreviewStale(t *testing.T, jr *jobRunner) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(jr.repositories[1].WorkDir, "final.txt"), []byte("final\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func eventIndex(captured *capturedEvents, kind string) int {
	captured.mu.Lock()
	defer captured.mu.Unlock()
	for index, event := range captured.events {
		if event["kind"] == kind {
			return index
		}
	}
	return -1
}

func assertNoPreviewEvidence(t *testing.T, captured *capturedEvents) {
	t.Helper()
	if events := captured.byKind("preview"); len(events) != 0 {
		t.Fatalf("preview evidence events = %v", events)
	}
}

func assertReviewReadySent(t *testing.T, captured *capturedEvents) {
	t.Helper()
	if events := captured.byKind("review_ready"); len(events) != 1 {
		t.Fatalf("review_ready events = %v", events)
	}
}

func TestFinalizeBuildPublishesNoEvidenceWithoutDeploy(t *testing.T) {
	jr, captured, _ := previewEvidenceFixture(t)
	if state := finalizeEvidenceBuild(t, jr, true); state != "succeeded" {
		t.Fatalf("state = %s", state)
	}
	assertNoPreviewEvidence(t, captured)
	assertNoPreviewDeployRequest(t, jr, captured)
	assertReviewReadySent(t, captured)
}

func TestFinalizeBuildPublishesEvidenceForCurrentDeploy(t *testing.T) {
	jr, captured, want := previewEvidenceFixture(t)
	expiresAt := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond).UTC()
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, expiresAt)
	})
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	if state := finalizeEvidenceBuild(t, jr, true); state != "succeeded" {
		t.Fatalf("state = %s", state)
	}
	if requests := captured.byKind(v1.EventPreviewDeployRequested); len(requests) != 1 {
		t.Fatalf("request events = %d", len(requests))
	}
	events := captured.byKind("preview")
	if len(events) != 1 {
		t.Fatalf("preview events = %v", events)
	}
	evidence := events[0]
	if evidence["url"] != "https://"+previewDeployToolEdgeHost {
		t.Fatalf("url = %v", evidence["url"])
	}
	if evidence["expires_at"] != expiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("expires_at = %v want %s", evidence["expires_at"], expiresAt.Format(time.RFC3339Nano))
	}
	if evidence["health"] != "healthy" {
		t.Fatalf("health = %v", evidence["health"])
	}
	if limitations, ok := evidence["known_limitations"].([]string); !ok || limitations == nil || len(limitations) != 0 {
		t.Fatalf("known_limitations = %#v", evidence["known_limitations"])
	}
	commitSet, ok := evidence["commit_set"].(map[string]string)
	if !ok || len(commitSet) != len(want) {
		t.Fatalf("commit_set = %#v", evidence["commit_set"])
	}
	for repositoryID, sha := range want {
		if commitSet[repositoryID] != sha {
			t.Fatalf("commit_set = %v want %v", commitSet, want)
		}
	}
	if evidence["manifest_id"] != jr.job.ManifestID || evidence["manifest_digest"] != jr.job.ManifestDigest || evidence["attempt_id"] != jr.job.AttemptID {
		t.Fatalf("evidence is not manifest bound: %v", evidence)
	}
	commits, err := v1.PreviewCommitSetFromMap(want)
	if err != nil {
		t.Fatal(err)
	}
	eventID := v1.PreviewJobEvidenceEventID(jr.job.JobID, jr.job.LeaseID, v1.PreviewLifecycleCommitSetDigest(commits))
	if has, err := jr.d.journal.HasRecord(context.Background(), eventID); err != nil || !has {
		t.Fatalf("evidence event %s journaled = %v err = %v", eventID, has, err)
	}
	preview, reviewReady := eventIndex(captured, "preview"), eventIndex(captured, "review_ready")
	if preview < 0 || reviewReady < 0 || preview > reviewReady {
		t.Fatalf("preview index = %d review_ready index = %d", preview, reviewReady)
	}
}

func TestFinalizeBuildRedeploysStalePreviewBeforeEvidence(t *testing.T) {
	jr, captured, want := previewEvidenceFixture(t)
	controller := newFakeJobNetworkController()
	jr.d.previewHostController = controller
	firstExpiry := time.Now().Add(time.Hour).Truncate(time.Millisecond).UTC()
	finalExpiry := time.Now().Add(3 * time.Hour).Truncate(time.Millisecond).UTC()
	var requests []map[string]any
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		requests = append(requests, event)
		if len(requests) == 1 {
			deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, firstExpiry)
			return
		}
		deployPreviewResultAt(jr, event, previewEvidenceRedeployEdgeHost, finalExpiry)
	})
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	attachesBefore := len(controller.attachCalls())
	makePreviewStale(t, jr)
	if state := finalizeEvidenceBuild(t, jr, true); state != "succeeded" {
		t.Fatalf("state = %s", state)
	}
	if len(requests) != 2 {
		t.Fatalf("request events = %d", len(requests))
	}
	if got, want := previewDeployRequestedID(requests[1]), v1.PreviewDeployRequestID(jr.job.JobID, jr.job.LeaseID, 2); got != want {
		t.Fatalf("second request id = %s want %s", got, want)
	}
	if got := len(controller.attachCalls()); got != attachesBefore {
		t.Fatalf("finalize attached the job network: %d -> %d", attachesBefore, got)
	}
	events := captured.byKind("preview")
	if len(events) != 1 {
		t.Fatalf("preview events = %v", events)
	}
	evidence := events[0]
	if evidence["url"] != "https://"+previewEvidenceRedeployEdgeHost {
		t.Fatalf("url = %v", evidence["url"])
	}
	if evidence["expires_at"] != finalExpiry.Format(time.RFC3339Nano) {
		t.Fatalf("expires_at = %v", evidence["expires_at"])
	}
	commitSet, _ := evidence["commit_set"].(map[string]string)
	if commitSet["primary"] != want["primary"] || commitSet["secondary"] == want["secondary"] || len(commitSet) != 2 {
		t.Fatalf("commit_set = %v initial = %v", commitSet, want)
	}
	if digest, _ := requests[1]["commit_set_digest"].(string); digest == "" {
		t.Fatal("redeploy request carries no digest")
	} else if has, err := jr.d.journal.HasRecord(context.Background(), v1.PreviewJobEvidenceEventID(jr.job.JobID, jr.job.LeaseID, digest)); err != nil || !has {
		t.Fatalf("evidence id for final digest journaled = %v err = %v", has, err)
	}
	if preview, reviewReady := eventIndex(captured, "preview"), eventIndex(captured, "review_ready"); preview < 0 || preview > reviewReady {
		t.Fatalf("preview index = %d review_ready index = %d", preview, reviewReady)
	}
}

func TestFinalizeBuildSkipsEvidenceWhenRedeployFails(t *testing.T) {
	jr, captured, _ := previewEvidenceFixture(t)
	requests := 0
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		requests++
		if requests == 1 {
			deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, time.Now().Add(time.Hour).Truncate(time.Millisecond))
			return
		}
		rejectPreviewDeployRequest(jr, event, "no_approved_blueprint")
	})
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	makePreviewStale(t, jr)
	if state := finalizeEvidenceBuild(t, jr, true); state != "succeeded" {
		t.Fatalf("state = %s", state)
	}
	if requests != 2 {
		t.Fatalf("request events = %d", requests)
	}
	assertNoPreviewEvidence(t, captured)
	assertReviewReadySent(t, captured)
}

func TestFinalizeBuildSkipsRedeployAtLimit(t *testing.T) {
	jr, captured, _ := previewEvidenceFixture(t)
	requests := 0
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		requests++
		if requests == 1 {
			deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, time.Now().Add(time.Hour).Truncate(time.Millisecond))
			return
		}
		rejectPreviewDeployRequest(jr, event, "no_approved_blueprint")
	})
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	for range v1.PreviewJobDeployMaxRequests - 1 {
		assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusRefused, "no_approved_blueprint")
	}
	makePreviewStale(t, jr)
	if state := finalizeEvidenceBuild(t, jr, true); state != "succeeded" {
		t.Fatalf("state = %s", state)
	}
	if requests != v1.PreviewJobDeployMaxRequests || len(captured.byKind(v1.EventPreviewDeployRequested)) != v1.PreviewJobDeployMaxRequests {
		t.Fatalf("request events = %d", requests)
	}
	if got := len(journaledPreviewDeployRequests(t, jr)); got != v1.PreviewJobDeployMaxRequests {
		t.Fatalf("journaled requests = %d", got)
	}
	assertNoPreviewEvidence(t, captured)
	assertReviewReadySent(t, captured)
}

func TestFinalizeBuildNoEvidenceWhenChecksFail(t *testing.T) {
	jr, captured, _ := previewEvidenceFixture(t)
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, time.Now().Add(time.Hour).Truncate(time.Millisecond))
	})
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	makePreviewStale(t, jr)
	jr.job.RequiredChecks = []string{"unregistered-check"}
	if state := finalizeEvidenceBuild(t, jr, true); state != "failed" {
		t.Fatalf("state = %s", state)
	}
	if requests := captured.byKind(v1.EventPreviewDeployRequested); len(requests) != 1 {
		t.Fatalf("request events = %d", len(requests))
	}
	assertNoPreviewEvidence(t, captured)
	if events := captured.byKind("review_ready"); len(events) != 0 {
		t.Fatalf("review_ready events = %v", events)
	}
}

func TestFinalizeBuildNoEvidenceWithoutReviewReadyIntent(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "stale"}[stale], func(t *testing.T) {
			jr, captured, _ := previewEvidenceFixture(t)
			jr.reviewReadyIntent = nil
			respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
				deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, time.Now().Add(time.Hour).Truncate(time.Millisecond))
			})
			assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
			if stale {
				makePreviewStale(t, jr)
			}
			if state := finalizeEvidenceBuild(t, jr, true); state != "succeeded" {
				t.Fatalf("state = %s", state)
			}
			if requests := captured.byKind(v1.EventPreviewDeployRequested); len(requests) != 1 {
				t.Fatalf("request events = %d", len(requests))
			}
			if sequence := jr.previewDeploys.sequence; sequence != 1 {
				t.Fatalf("request slots used = %d", sequence)
			}
			assertNoPreviewEvidence(t, captured)
			if events := captured.byKind("review_ready"); len(events) != 0 {
				t.Fatalf("review_ready events = %v", events)
			}
		})
	}
}

func TestFinalizeBuildRedeploysExpiringPreview(t *testing.T) {
	cases := map[string]struct {
		firstExpiry func() time.Time
		clock       func() time.Time
	}{
		"expired": {
			firstExpiry: func() time.Time { return time.Now().Add(time.Hour) },
			clock:       func() time.Time { return time.Now().Add(2 * time.Hour) },
		},
		"inside margin": {
			firstExpiry: func() time.Time { return time.Now().Add(previewJobEvidenceExpiryMargin - time.Minute) },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			jr, captured, want := previewEvidenceFixture(t)
			finalExpiry := time.Now().Add(4 * time.Hour).Truncate(time.Millisecond).UTC()
			requests := 0
			respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
				requests++
				if requests == 1 {
					deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, tc.firstExpiry().Truncate(time.Millisecond))
					return
				}
				deployPreviewResultAt(jr, event, previewEvidenceRedeployEdgeHost, finalExpiry)
			})
			assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
			if tc.clock != nil {
				jr.d.now = tc.clock
			}
			if state := finalizeEvidenceBuild(t, jr, true); state != "succeeded" {
				t.Fatalf("state = %s", state)
			}
			if requests != 2 {
				t.Fatalf("request events = %d", requests)
			}
			events := captured.byKind("preview")
			if len(events) != 1 {
				t.Fatalf("preview events = %v", events)
			}
			evidence := events[0]
			if evidence["expires_at"] != finalExpiry.Format(time.RFC3339Nano) || evidence["url"] != "https://"+previewEvidenceRedeployEdgeHost {
				t.Fatalf("evidence = %v", evidence)
			}
			commitSet, _ := evidence["commit_set"].(map[string]string)
			for repositoryID, sha := range want {
				if commitSet[repositoryID] != sha {
					t.Fatalf("commit_set = %v want %v", commitSet, want)
				}
			}
			assertReviewReadySent(t, captured)
		})
	}
}

func currentPreviewEvidenceOutcome(t *testing.T, commitSet map[string]string, edgeHost string) previewJobDeployOutcome {
	t.Helper()
	commits, err := v1.PreviewCommitSetFromMap(commitSet)
	if err != nil {
		t.Fatal(err)
	}
	return previewJobDeployOutcome{
		Status: v1.PreviewJobDeployStatusDeployed, PreviewID: previewDeployToolPreviewID, EdgeHost: edgeHost,
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Millisecond), CommitSet: commitSet,
		CommitSetDigest: v1.PreviewLifecycleCommitSetDigest(commits), Sequence: 1,
	}
}

func TestPublishFinalPreviewEvidenceSkipsEmptyEdgeHost(t *testing.T) {
	jr, captured, want := previewEvidenceFixture(t)
	jr.previewDeploys.finish(currentPreviewEvidenceOutcome(t, want, ""))
	jr.publishFinalPreviewEvidence(context.Background(), want)
	assertNoPreviewEvidence(t, captured)
	assertNoPreviewDeployRequest(t, jr, captured)
}

func TestPublishFinalPreviewEvidenceSkipsWhenLeaseInvalid(t *testing.T) {
	jr, captured, want := previewEvidenceFixture(t)
	jr.previewDeploys.finish(currentPreviewEvidenceOutcome(t, want, previewDeployToolEdgeHost))
	jr.d.leases.Release(jr.job.AttemptID)
	jr.publishFinalPreviewEvidence(context.Background(), want)
	assertNoPreviewEvidence(t, captured)
	assertNoPreviewDeployRequest(t, jr, captured)
}

func TestFinalPreviewEvidenceReplayKeepsJournaledPayload(t *testing.T) {
	jr, captured, want := previewEvidenceFixture(t)
	logs := &bytes.Buffer{}
	jr.logger = slog.New(slog.NewTextHandler(&lockedWriter{buffer: logs}, nil))
	first := currentPreviewEvidenceOutcome(t, want, previewDeployToolEdgeHost)
	jr.previewDeploys.finish(first)
	jr.publishFinalPreviewEvidence(context.Background(), want)
	eventID := v1.PreviewJobEvidenceEventID(jr.job.JobID, jr.job.LeaseID, first.CommitSetDigest)
	original, ok, err := jr.d.journal.LoadRecord(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("evidence record loaded = %v err = %v", ok, err)
	}
	if !bytes.Contains(original.Payload, []byte(first.ExpiresAt.UTC().Format(time.RFC3339Nano))) {
		t.Fatalf("journaled evidence = %s", original.Payload)
	}
	second := first
	second.EdgeHost = previewEvidenceRedeployEdgeHost
	second.ExpiresAt = first.ExpiresAt.Add(time.Hour)
	second.Sequence = 2
	jr.previewDeploys.finish(second)
	jr.publishFinalPreviewEvidence(context.Background(), want)
	replayed, ok, err := jr.d.journal.LoadRecord(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("evidence record reloaded = %v err = %v", ok, err)
	}
	if !bytes.Equal(replayed.Payload, original.Payload) {
		t.Fatalf("journaled evidence changed:\n%s\n%s", original.Payload, replayed.Payload)
	}
	if events := captured.byKind("preview"); len(events) != 1 {
		t.Fatalf("preview events = %d", len(events))
	}
	if !strings.Contains(logs.String(), "final preview evidence dropped") || !strings.Contains(logs.String(), eventID) {
		t.Fatalf("logs = %s", logs.String())
	}
	assertNoPreviewDeployRequest(t, jr, captured)
}

func TestFinalizeBuildCancelledDuringRedeployPublishesNoEvidence(t *testing.T) {
	jr, captured, _ := previewEvidenceFixture(t)
	requests := 0
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		requests++
		if requests == 1 {
			deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, time.Now().Add(time.Hour).Truncate(time.Millisecond))
			return
		}
		jr.cancel()
	})
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
	makePreviewStale(t, jr)
	rm := repo.NewManager(filepath.Join(jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	done := make(chan struct{})
	var state string
	var err error
	go func() {
		defer close(done)
		state, _, err = jr.finalizeBuild(jr.ctx, rm, true)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("finalize did not return after cancellation")
	}
	if requests != 2 {
		t.Fatalf("request events = %d", requests)
	}
	assertNoPreviewEvidence(t, captured)
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
	if state != "failed" || !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "publish review readiness: ") {
		t.Fatalf("state = %q err = %v", state, err)
	}
	if events := captured.byKind("review_ready"); len(events) != 0 {
		t.Fatalf("review_ready events = %v", events)
	}
	if outcome := buildFinalizationFailure(err); outcome.State != v1.JobFinishedFailed || outcome.FailureCode != v1.JobFailureBuildFinalization || outcome.FailureMessage != "build finalization failed" {
		t.Fatalf("job outcome = %+v", outcome)
	}
}

func TestFinalizeBuildAgentExitPublishesNoEvidence(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "stale"}[stale], func(t *testing.T) {
			jr, captured, _ := previewEvidenceFixture(t)
			respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
				deployPreviewResultAt(jr, event, previewDeployToolEdgeHost, time.Now().Add(time.Hour).Truncate(time.Millisecond))
			})
			assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusDeployed, "")
			if stale {
				makePreviewStale(t, jr)
			}
			if state := finalizeEvidenceBuild(t, jr, false); state != "succeeded" {
				t.Fatalf("state = %s", state)
			}
			if requests := captured.byKind(v1.EventPreviewDeployRequested); len(requests) != 1 {
				t.Fatalf("request events = %d", len(requests))
			}
			assertNoPreviewEvidence(t, captured)
		})
	}
}
