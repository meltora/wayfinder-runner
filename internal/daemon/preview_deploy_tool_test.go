package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/repo"
)

const previewDeployToolEdgeHost = "abcdefghijklmnopqrstuvwxyz.previews.example.test"

type previewDeployResponder func(event map[string]any)

func deployPreviewFixture(t *testing.T) (*jobRunner, *capturedEvents, map[string]string) {
	t.Helper()
	jr, captured, want := previewFixture(t)
	jr.d.cfg.AllowUnsafeDirect = true
	jr.d.cfg.Isolation = "docker"
	jr.d.cfg.JobNetwork = "restricted"
	jr.d.cfg.JobNetworkName = "job-network"
	jr.d.previewHostController = &fakePreviewHostController{}
	jr.d.previewTunnelAdvertised.Store(true)
	manager := jr.d.previewHostRepositoryManager()
	for index := range jr.repositories {
		repository := &jr.repositories[index]
		origin := filepath.Join(t.TempDir(), "origin.git")
		agentGit(t, repository.WorkDir, "clone", "-q", "--bare", repository.WorkDir, origin)
		repository.URL = origin
		if _, err := manager.Register(context.Background(), origin); err != nil {
			t.Fatal(err)
		}
	}
	return jr, captured, want
}

func respondToPreviewDeployRequests(jr *jobRunner, captured *capturedEvents, respond previewDeployResponder) {
	jr.d.eventSink = func(ctx context.Context, payload any) error {
		if err := captured.sink(ctx, payload); err != nil {
			return err
		}
		event, ok := payload.(map[string]any)
		if ok && event["kind"] == v1.EventPreviewDeployRequested && respond != nil {
			respond(event)
		}
		return nil
	}
}

func previewDeployRequestedID(event map[string]any) string {
	requestID, _ := event["request_id"].(string)
	return requestID
}

func deployPreviewResult(jr *jobRunner, event map[string]any, expiresAt time.Time) {
	digest, _ := event["commit_set_digest"].(string)
	result := v1.PreviewDeployResultEvent{Outcome: deployedPreviewOutcome()}
	result.IdempotencyKey = v1.PreviewJobDeployIdempotencyKey(previewDeployRequestedID(event))
	result.ExecutionManifestID = jr.job.ManifestID
	result.CommitSetDigest = digest
	result.EdgeHost = previewDeployToolEdgeHost
	result.ExpiresAt = expiresAt
	jr.d.previewJobDeployResultSent(result)
}

func rejectPreviewDeployRequest(jr *jobRunner, event map[string]any, code string) {
	jr.d.previewJobDeployEventRejected(v1.PreviewDeployRequestedEventID(previewDeployRequestedID(event)), code)
}

func callDeployPreview(t *testing.T, jr *jobRunner) previewDeployToolReply {
	t.Helper()
	result, err := call(t, jr, mcpbridge.DeployPreviewTool, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := result.(previewDeployToolReply)
	if !ok {
		t.Fatalf("reply type = %T", result)
	}
	return reply
}

func assertPreviewDeployReply(t *testing.T, reply previewDeployToolReply, status, code string) {
	t.Helper()
	if reply.Status != status || reply.Code != code {
		t.Fatalf("reply = %+v want %s/%s", reply, status, code)
	}
}

func journaledPreviewDeployRequests(t *testing.T, jr *jobRunner) []string {
	t.Helper()
	records, err := jr.d.journal.Unacked(context.Background(), journal.KindEvent, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, record := range records {
		var envelope struct {
			Payload struct {
				Kind string `json:"kind"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(record.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Payload.Kind == v1.EventPreviewDeployRequested {
			ids = append(ids, record.ID)
		}
	}
	return ids
}

func assertNoPreviewDeployRequest(t *testing.T, jr *jobRunner, captured *capturedEvents) {
	t.Helper()
	if events := captured.byKind(v1.EventPreviewDeployRequested); len(events) != 0 {
		t.Fatalf("request events = %v", events)
	}
	if ids := journaledPreviewDeployRequests(t, jr); len(ids) != 0 {
		t.Fatalf("journaled request events = %v", ids)
	}
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestDeployPreviewListedOnlyWithHostingAndTunnel(t *testing.T) {
	for _, hosting := range []bool{false, true} {
		for _, tunnel := range []bool{false, true} {
			jr, _ := testJobRunner(t)
			if hosting {
				jr.d.previewHostController = &fakePreviewHostController{}
			}
			jr.d.previewTunnelAdvertised.Store(tunnel)
			listed, err := call(t, jr, mcpbridge.InternalListToolsTool, `{}`)
			if err != nil {
				t.Fatal(err)
			}
			contextResult, err := call(t, jr, "platform_get_job_context", `{}`)
			if err != nil {
				t.Fatal(err)
			}
			want := hosting && tunnel
			for _, tools := range [][]string{listed.([]string), contextResult.(map[string]any)["capabilities"].([]string)} {
				if slices.Contains(tools, mcpbridge.DeployPreviewTool) != want {
					t.Fatalf("hosting=%v tunnel=%v listed=%v", hosting, tunnel, tools)
				}
			}
			jr.job.Kind = v1.JobKindPlan
			if slices.Contains(jr.d.capabilitiesForJob(v1.JobKindPlan), mcpbridge.DeployPreviewTool) {
				t.Fatalf("plan job lists %s", mcpbridge.DeployPreviewTool)
			}
		}
	}
}

func TestDeployPreviewRefusedWithoutCapabilityAndSendsNothing(t *testing.T) {
	for name, configure := range map[string]func(*jobRunner){
		"no hosting": func(jr *jobRunner) { jr.d.previewHostController = nil },
		"no tunnel":  func(jr *jobRunner) { jr.d.previewTunnelAdvertised.Store(false) },
		"invalid hosting": func(jr *jobRunner) {
			jr.d.previewHostController = &fakePreviewHostController{capability: &v1.PreviewHostingCapability{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			jr, captured, _ := deployPreviewFixture(t)
			configure(jr)
			reply := callDeployPreview(t, jr)
			assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusRefused, previewJobDeployCodeCapabilityMissing)
			assertNoPreviewDeployRequest(t, jr, captured)
		})
	}
}

func TestDeployPreviewRefusedWithoutJobNetwork(t *testing.T) {
	for name, configure := range map[string]func(*jobRunner){
		"direct isolation": func(jr *jobRunner) { jr.d.cfg.Isolation = "direct" },
		"network none":     func(jr *jobRunner) { jr.d.cfg.JobNetwork = "none" },
		"no network name":  func(jr *jobRunner) { jr.d.cfg.JobNetworkName = "" },
	} {
		t.Run(name, func(t *testing.T) {
			jr, captured, _ := deployPreviewFixture(t)
			configure(jr)
			reply := callDeployPreview(t, jr)
			assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusRefused, previewJobDeployCodeNetworkUnsupported)
			assertNoPreviewDeployRequest(t, jr, captured)
		})
	}
}

func TestDeployPreviewRejectsNonEmptyArgs(t *testing.T) {
	jr, captured, _ := deployPreviewFixture(t)
	for _, args := range []string{`{"idempotency_key":"deploy-1"}`, `{"url":"https://example.test"}`, `{"commit_sha":"` + strings.Repeat("a", 40) + `"}`} {
		if _, err := call(t, jr, mcpbridge.DeployPreviewTool, args); err == nil || !strings.Contains(err.Error(), "must be empty") {
			t.Fatalf("args %s err = %v", args, err)
		}
	}
	assertNoPreviewDeployRequest(t, jr, captured)
}

func TestDeployPreviewRefusedForPlanJob(t *testing.T) {
	jr, captured, _ := deployPreviewFixture(t)
	jr.job.Kind = v1.JobKindPlan
	jr.job.ApprovedPlanRevision = 0
	if _, err := call(t, jr, mcpbridge.DeployPreviewTool, `{}`); err == nil || !strings.Contains(err.Error(), "is not allowed for plan jobs") {
		t.Fatalf("plan job err = %v", err)
	}
	assertNoPreviewDeployRequest(t, jr, captured)
}

func TestDeployPreviewEmitsRequestedEventWithVectorIDs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "runner", "v1", "testdata", "preview_job_deploy_v1_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		RequestIDs []struct {
			JobID     string `json:"job_id"`
			LeaseID   string `json:"lease_id"`
			Sequence  uint64 `json:"sequence"`
			RequestID string `json:"request_id"`
			EventID   string `json:"event_id"`
		} `json:"request_ids"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	vector := vectors.RequestIDs[0]
	if vector.Sequence != 1 {
		t.Fatalf("vector sequence = %d", vector.Sequence)
	}
	jr, captured, want := deployPreviewFixture(t)
	jr.job.JobID, jr.job.LeaseID = vector.JobID, vector.LeaseID
	expiresAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Millisecond)
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) { deployPreviewResult(jr, event, expiresAt) })
	before := time.Now().UTC().Truncate(time.Millisecond)
	reply := callDeployPreview(t, jr)
	assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusDeployed, "")
	events := captured.byKind(v1.EventPreviewDeployRequested)
	if len(events) != 1 {
		t.Fatalf("request events = %v", events)
	}
	event := events[0]
	if event["request_id"] != vector.RequestID || event["schema_version"] != v1.PreviewDeployRequestedSchemaVersion ||
		event["job_id"] != vector.JobID || event["lease_id"] != vector.LeaseID || event["fencing_token"] != jr.job.FencingToken {
		t.Fatalf("event = %v", event)
	}
	if ids := journaledPreviewDeployRequests(t, jr); len(ids) != 1 || ids[0] != vector.EventID {
		t.Fatalf("journaled ids = %v want %s", ids, vector.EventID)
	}
	for field, value := range map[string]any{
		"manifest_id": jr.job.ManifestID, "manifest_digest": jr.job.ManifestDigest,
		"attempt_id": jr.job.AttemptID, "manifest_kind": string(jr.job.Kind),
	} {
		if event[field] != value {
			t.Fatalf("%s = %v want %v", field, event[field], value)
		}
	}
	observedAt, err := time.Parse(time.RFC3339Nano, event["observed_at"].(string))
	if err != nil || observedAt.Before(before) {
		t.Fatalf("observed_at = %v err = %v", event["observed_at"], err)
	}
	commits, ok := event["commit_set"].([]v1.PreviewObservationCommit)
	if !ok || len(commits) != 2 || commits[0].RepositoryID != "primary" || commits[1].RepositoryID != "secondary" ||
		commits[0].CommitSHA != want["primary"] || commits[1].CommitSHA != want["secondary"] {
		t.Fatalf("commit_set = %#v", event["commit_set"])
	}
	if event["commit_set_digest"] != v1.PreviewLifecycleCommitSetDigest(commits) {
		t.Fatalf("commit_set_digest = %v", event["commit_set_digest"])
	}
	requestedAt, err := time.Parse(time.RFC3339Nano, event["requested_at"].(string))
	if err != nil || v1.FormatPreviewJobDeployTime(requestedAt) != event["requested_at"] || requestedAt.Before(before) {
		t.Fatalf("requested_at = %v err = %v", event["requested_at"], err)
	}
	if !strings.HasPrefix(event["observed_at"].(string), strings.TrimSuffix(event["requested_at"].(string), "Z")) {
		t.Fatalf("observed_at %v does not match requested_at %v", event["observed_at"], event["requested_at"])
	}
	last := jr.previewDeploys.lastDeployed()
	if last == nil || last.Sequence != 1 || last.EdgeHost != previewDeployToolEdgeHost || !last.ExpiresAt.Equal(expiresAt) ||
		last.CommitSetDigest != event["commit_set_digest"] {
		t.Fatalf("last deploy = %+v", last)
	}
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestDeployPreviewReplyIsDeployedShapeWithoutEdgeHost(t *testing.T) {
	jr, captured, want := deployPreviewFixture(t)
	expiresAt := time.Date(2026, 11, 25, 13, 14, 15, 678_000_000, time.UTC)
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) { deployPreviewResult(jr, event, expiresAt) })
	encoded, err := json.Marshal(callDeployPreview(t, jr))
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"status":"deployed","url":"http://our-preview:8080/","expires_at":"2026-11-25T13:14:15.678Z","commit_set":{"primary":"` +
		want["primary"] + `","secondary":"` + want["secondary"] + `"}}`
	if string(encoded) != expected {
		t.Fatalf("reply = %s\nwant %s", encoded, expected)
	}
	if strings.Contains(string(encoded), "previews.example.test") {
		t.Fatalf("reply leaks the edge host: %s", encoded)
	}
}

func TestDeployPreviewRecordsTrustedCommitSetBeforeSending(t *testing.T) {
	jr, captured, _ := deployPreviewFixture(t)
	recorded := false
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		commits := event["commit_set"].([]v1.PreviewObservationCommit)
		ok, err := jr.d.journal.TrustedCommitSetRecorded(context.Background(), jr.job.ManifestID, jr.job.ManifestDigest,
			event["commit_set_digest"].(string), commits)
		recorded = err == nil && ok
		rejectPreviewDeployRequest(jr, event, "no_approved_blueprint")
	})
	reply := callDeployPreview(t, jr)
	assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusRefused, "no_approved_blueprint")
	if !recorded {
		t.Fatal("trusted commit set was not recorded before the request was sent")
	}
	if jr.previewDeploys.lastDeployed() != nil {
		t.Fatal("a refused request was recorded as deployed")
	}
}

func TestDeployPreviewImportsUnpushedCommitIntoMirror(t *testing.T) {
	jr, captured, want := deployPreviewFixture(t)
	secondary := jr.repositories[1]
	if err := os.WriteFile(filepath.Join(secondary.WorkDir, "unpushed.txt"), []byte("unpushed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := jr.d.previewHostRepositoryManager()
	mirrorHeads := map[string]string{}
	mirrors := map[string]string{}
	for _, repository := range jr.repositories {
		mirror, err := manager.Register(context.Background(), repository.URL)
		if err != nil {
			t.Fatal(err)
		}
		mirrors[repository.ID] = mirror
		mirrorHeads[repository.ID] = trustedGitForTest(t, mirror, "for-each-ref", "refs/heads")
	}
	var sent map[string]string
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		sent = map[string]string{}
		for _, commit := range event["commit_set"].([]v1.PreviewObservationCommit) {
			sent[commit.RepositoryID] = commit.CommitSHA
		}
		if manager.HasCommit(context.Background(), secondary.URL, sent["secondary"]) {
			deployPreviewResult(jr, event, time.Now().Add(2*time.Hour).Truncate(time.Millisecond))
		}
	})
	reply := callDeployPreview(t, jr)
	assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusDeployed, "")
	if sent["primary"] != want["primary"] || sent["secondary"] == want["secondary"] {
		t.Fatalf("sent = %v clean heads = %v", sent, want)
	}
	if got := trustedGitForTest(t, mirrors["secondary"], "rev-parse", repo.PreviewMirrorRefPrefix+sent["secondary"]); got != sent["secondary"] {
		t.Fatalf("mirror preview ref = %s", got)
	}
	for _, repository := range jr.repositories {
		if after := trustedGitForTest(t, mirrors[repository.ID], "for-each-ref", "refs/heads"); after != mirrorHeads[repository.ID] {
			t.Fatalf("%s mirror refs/heads changed:\n%s\n%s", repository.ID, mirrorHeads[repository.ID], after)
		}
		if exported := trustedGitForTest(t, repository.Trusted.gitDir, "for-each-ref", repo.PreviewExportRefPrefix); exported != "" {
			t.Fatalf("%s trusted repository keeps export refs: %s", repository.ID, exported)
		}
	}
	if refs := trustedGitForTest(t, mirrors["primary"], "for-each-ref", repo.PreviewMirrorRefPrefix); refs != "" {
		t.Fatalf("an already mirrored commit was imported again: %s", refs)
	}
}

func TestPreviewDeployToolEnforcesSixRequestsAndOneInFlight(t *testing.T) {
	jr, captured, _ := deployPreviewFixture(t)
	var mu sync.Mutex
	var first map[string]any
	sent := make(chan struct{})
	respondToPreviewDeployRequests(jr, captured, func(event map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		if first == nil {
			first = event
			close(sent)
			return
		}
		rejectPreviewDeployRequest(jr, event, "no_approved_blueprint")
	})
	firstReply := make(chan previewDeployToolReply, 1)
	go func() {
		result, _ := jr.handleToolCall(context.Background(), mcpbridge.DeployPreviewTool, json.RawMessage(`{}`))
		reply, _ := result.(previewDeployToolReply)
		firstReply <- reply
	}()
	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("first request was not sent")
	}
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusRefused, previewJobDeployCodeInFlight)
	if got := len(journaledPreviewDeployRequests(t, jr)); got != 1 {
		t.Fatalf("in-flight call journaled %d requests", got)
	}
	mu.Lock()
	rejectPreviewDeployRequest(jr, first, "commit_set_mismatch")
	mu.Unlock()
	select {
	case reply := <-firstReply:
		assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusRefused, "commit_set_mismatch")
	case <-time.After(10 * time.Second):
		t.Fatal("first request did not finish")
	}
	for range v1.PreviewJobDeployMaxRequests - 1 {
		assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusRefused, "no_approved_blueprint")
	}
	assertPreviewDeployReply(t, callDeployPreview(t, jr), v1.PreviewJobDeployStatusRefused, previewJobDeployCodeLimitReached)
	ids := journaledPreviewDeployRequests(t, jr)
	if len(ids) != v1.PreviewJobDeployMaxRequests || len(captured.byKind(v1.EventPreviewDeployRequested)) != v1.PreviewJobDeployMaxRequests {
		t.Fatalf("journaled requests = %d", len(ids))
	}
	for sequence := uint64(1); sequence <= v1.PreviewJobDeployMaxRequests; sequence++ {
		eventID := v1.PreviewDeployRequestedEventID(v1.PreviewDeployRequestID(jr.job.JobID, jr.job.LeaseID, sequence))
		if !slices.Contains(ids, eventID) {
			t.Fatalf("sequence %d event %s missing from %v", sequence, eventID, ids)
		}
	}
	if outcome := jr.requestPreviewJobDeploy(context.Background(), map[string]string{"primary": strings.Repeat("a", 40)}, false); outcome.Status != v1.PreviewJobDeployStatusRefused || outcome.Code != previewJobDeployCodeLimitReached {
		t.Fatalf("finalize-style request outcome = %+v", outcome)
	}
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestDeployPreviewInvalidRepositoryIDReportsCommitSetUnavailable(t *testing.T) {
	t.Run("resolved inventory", func(t *testing.T) {
		jr, captured, _ := deployPreviewFixture(t)
		jr.repositories[0].ID = "org/repo"
		reply := callDeployPreview(t, jr)
		assertPreviewDeployReply(t, reply, v1.PreviewJobDeployStatusFailed, previewJobDeployCodeCommitSetUnavailable)
		assertNoPreviewDeployRequest(t, jr, captured)
	})
	t.Run("supplied commit set", func(t *testing.T) {
		jr, captured, want := deployPreviewFixture(t)
		set := map[string]string{"org/repo": want["primary"]}
		outcome := jr.requestPreviewJobDeploy(context.Background(), set, false)
		assertPreviewJobDeployOutcome(t, outcome, v1.PreviewJobDeployStatusFailed, previewJobDeployCodeCommitSetUnavailable)
		assertNoPreviewDeployRequest(t, jr, captured)
		commits := []v1.PreviewObservationCommit{{RepositoryID: "org/repo", CommitSHA: want["primary"]}}
		recorded, err := jr.d.journal.TrustedCommitSetRecorded(context.Background(), jr.job.ManifestID, jr.job.ManifestDigest,
			v1.PreviewLifecycleCommitSetDigest(commits), commits)
		if err != nil || recorded {
			t.Fatalf("invalid commit set recorded=%v err=%v", recorded, err)
		}
		if jr.previewDeploys.sequence != 0 || jr.previewDeploys.inFlight {
			t.Fatalf("local failure consumed a request: %+v", &jr.previewDeploys)
		}
	})
}

func TestDeployPreviewReplyShapes(t *testing.T) {
	expiresAt := time.Date(2026, 11, 25, 13, 14, 15, 678_900_000, time.FixedZone("CET", 3600))
	tests := map[string]struct {
		outcome previewJobDeployOutcome
		want    string
	}{
		"deployed": {
			outcome: previewJobDeployOutcome{
				Status: v1.PreviewJobDeployStatusDeployed, EdgeHost: previewDeployToolEdgeHost, ExpiresAt: expiresAt,
				CommitSet: map[string]string{"web": strings.Repeat("b", 40), "api": strings.Repeat("a", 40)},
				Sequence:  2, CommitSetDigest: strings.Repeat("c", 64),
			},
			want: `{"status":"deployed","url":"http://our-preview:8080/","expires_at":"2026-11-25T12:14:15.678Z","commit_set":{"api":"` +
				strings.Repeat("a", 40) + `","web":"` + strings.Repeat("b", 40) + `"}}`,
		},
		"refused": {
			outcome: previewJobDeployOutcome{Status: v1.PreviewJobDeployStatusRefused, Code: "deploy_limit_reached", Sequence: 6},
			want:    `{"status":"refused","code":"deploy_limit_reached"}`,
		},
		"failed": {
			outcome: previewJobDeployOutcome{Status: v1.PreviewJobDeployStatusFailed, Code: "health", EdgeHost: previewDeployToolEdgeHost,
				CommitSet: map[string]string{"api": strings.Repeat("a", 40)}},
			want: `{"status":"failed","code":"health"}`,
		},
		"timeout": {
			outcome: previewJobDeployOutcome{Status: v1.PreviewJobDeployStatusTimeout, Code: previewJobDeployCodeCommandTimeout},
			want:    `{"status":"timeout","code":"command_timeout"}`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(newPreviewDeployToolReply(test.outcome))
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.want {
				t.Fatalf("reply = %s\nwant %s", encoded, test.want)
			}
		})
	}
}
