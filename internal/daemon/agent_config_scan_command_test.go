package daemon

import (
	"context"
	"encoding/json"
	"errors"
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/scanner"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validScanCommand() v1.CommandPayload {
	a := v1.AgentConfigScanAuthority{AuthorityVersion: v1.AgentConfigScanTransportVersion, RequestID: "12345678-1234-4234-8234-123456789abc", AttemptID: "attempt-1", ProjectID: "project-1", RepositoryID: "repository-1", BranchRef: "refs/heads/main", LeaseID: "lease-scan-1", FencingToken: 1, LeaseExpiresAt: time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second).Format(time.RFC3339)}
	a.CommandDigest = v1.AgentConfigScanCommandDigest(a)
	return v1.CommandPayload{Command: v1.CmdAgentConfigScan, AgentConfigScan: &a}
}
func scanDaemon(t *testing.T) *Daemon {
	t.Helper()
	jr, _ := testJobRunner(t)
	d := jr.d
	d.runnerID = "runner-1"
	d.leases.Release("a1")
	d.activeJob = nil
	d.cfg.Repositories = map[string]config.RepositoryConfig{"repository-1": {URL: "https://example.invalid/repository.git"}}
	return d
}
func scanEnvelope(command v1.CommandPayload) v1.Envelope {
	return commandEnvelope(v1.AgentConfigScanCommandMessageID(command.AgentConfigScan.RequestID), command)
}
func scanResult(t *testing.T, d *Daemon, id string) v1.AgentConfigScanResultEvent {
	t.Helper()
	record, ok, err := d.journal.LoadRecord(context.Background(), v1.AgentConfigScanResultEventID(id))
	if err != nil || !ok {
		t.Fatalf("missing durable result: %v", err)
	}
	var env struct {
		Payload v1.AgentConfigScanResultEvent `json:"payload"`
	}
	if err := json.Unmarshal(record.Payload, &env); err != nil {
		t.Fatal(err)
	}
	return env.Payload
}
func TestAgentConfigScanBoundaryRejectsSmuggledAuthority(t *testing.T) {
	c := validScanCommand()
	if err := validateAgentConfigScanCommandBoundary(c); err != nil {
		t.Fatal(err)
	}
	c.JobID = "job-1"
	if validateAgentConfigScanCommandBoundary(c) == nil {
		t.Fatal("job authority accepted")
	}
	c = validScanCommand()
	c.Command = v1.CmdResumeRunner
	if validateAgentConfigScanCommandBoundary(c) == nil {
		t.Fatal("cross-command scan accepted")
	}
	c = validScanCommand()
	c.AgentConfigScan = nil
	if validateAgentConfigScanCommandBoundary(c) == nil {
		t.Fatal("missing authority accepted")
	}
}
func TestAgentConfigScanDurableResultAndExactRedelivery(t *testing.T) {
	d := scanDaemon(t)
	var calls atomic.Int32
	d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		calls.Add(1)
		return strings.Repeat("a", 40), scanner.Inventory{Items: []v1.AgentConfigDiscoveryItem{}}, nil
	}
	c := validScanCommand()
	env := scanEnvelope(c)
	if err := d.OnCommand(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	result := scanResult(t, d, env.ID)
	if result.Outcome.Kind != "discovered" || result.Outcome.Scan.CommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("result=%+v", result)
	}
	if err := d.OnCommand(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	if calls.Load() != 1 {
		t.Fatal("redelivery rescanned")
	}
	c.AgentConfigScan.BranchRef = "refs/heads/other"
	c.AgentConfigScan.CommandDigest = v1.AgentConfigScanCommandDigest(*c.AgentConfigScan)
	env.Payload = c
	if err := d.OnCommand(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	if calls.Load() != 1 {
		t.Fatal("changed content rescanned")
	}
}
func TestAgentConfigScanEmergencyStopCancelsWithoutBlockingLoop(t *testing.T) {
	d := scanDaemon(t)
	started := make(chan struct{})
	d.agentConfigScanReader = func(ctx context.Context, _, _ string, _ *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		close(started)
		<-ctx.Done()
		return "", scanner.Inventory{}, ctx.Err()
	}
	c := validScanCommand()
	env := scanEnvelope(c)
	if err := d.OnCommand(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scan never started")
	}
	if err := d.OnCommand(context.Background(), commandEnvelope("stop-1", v1.CommandPayload{Command: v1.CmdEmergencyStop})); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	result := scanResult(t, d, env.ID)
	if result.Outcome.Code != "stopped" {
		t.Fatalf("outcome=%+v", result.Outcome)
	}
}
func TestAgentConfigScanProcessingRecoveryDoesNotRepeatGit(t *testing.T) {
	d := scanDaemon(t)
	d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		t.Error("uncertain operation rescanned")
		return "", scanner.Inventory{}, errors.New("unexpected")
	}
	c := validScanCommand()
	env := scanEnvelope(c)
	raw, _ := json.Marshal(env)
	digest, _ := commandContentDigest(c)
	if _, _, err := d.journal.ReceiveCommand(context.Background(), journal.Record{ID: env.ID, Kind: journal.KindCommand, Payload: raw, CreatedAt: time.Now()}, digest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.journal.ClaimCommand(context.Background(), env.ID, "previous-owner"); err != nil {
		t.Fatal(err)
	}
	if err := d.OnCommand(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	d.agentConfigScanCommands.Wait()
	if got := scanResult(t, d, env.ID).Outcome.Code; got != "interrupted" {
		t.Fatalf("outcome=%s", got)
	}
}

func TestAgentConfigScanFailureCodesAndNoPartialInventory(t *testing.T) {
	cases := map[string]error{"checkout_failed": errors.New("private transport diagnostic"), "repository_unavailable": repo.ErrScanInvalidInput, "scan_failed": scanner.ErrIncompleteScan, "limit_exceeded": repo.ErrScanLimit}
	for code, failure := range cases {
		t.Run(code, func(t *testing.T) {
			d := scanDaemon(t)
			d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
				return strings.Repeat("a", 40), scanner.Inventory{Items: []v1.AgentConfigDiscoveryItem{}}, failure
			}
			c := validScanCommand()
			env := scanEnvelope(c)
			_ = d.OnCommand(context.Background(), env)
			d.agentConfigScanCommands.Wait()
			out := scanResult(t, d, env.ID).Outcome
			if out.Kind != "failed" || out.Code != code || out.Scan != nil {
				t.Fatalf("outcome=%+v", out)
			}
		})
	}
	t.Run("unknown registration", func(t *testing.T) {
		d := scanDaemon(t)
		d.cfg.Repositories = nil
		c := validScanCommand()
		env := scanEnvelope(c)
		_ = d.OnCommand(context.Background(), env)
		d.agentConfigScanCommands.Wait()
		if scanResult(t, d, env.ID).Outcome.Code != "repository_unavailable" {
			t.Fatal("missing repository was scanned")
		}
	})
	t.Run("invalid reader output", func(t *testing.T) {
		d := scanDaemon(t)
		d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
			return "bad", scanner.Inventory{}, nil
		}
		c := validScanCommand()
		env := scanEnvelope(c)
		_ = d.OnCommand(context.Background(), env)
		d.agentConfigScanCommands.Wait()
		if scanResult(t, d, env.ID).Outcome.Code != "scan_failed" {
			t.Fatal("invalid inventory accepted")
		}
	})
}
func TestAgentConfigScanAdmissionAndReservation(t *testing.T) {
	cases := map[string]func(*Daemon, *v1.AgentConfigScanAuthority){
		"paused":    func(d *Daemon, _ *v1.AgentConfigScanAuthority) { d.paused.Store(true) },
		"draining":  func(d *Daemon, _ *v1.AgentConfigScanAuthority) { d.updateDrain = &pendingUpdate{} },
		"job":       func(d *Daemon, _ *v1.AgentConfigScanAuthority) { d.activeJob = &jobRunner{} },
		"proposal":  func(d *Daemon, _ *v1.AgentConfigScanAuthority) { d.workIntakeProposalActive = true },
		"recovered": func(d *Daemon, _ *v1.AgentConfigScanAuthority) { d.recoveredLease.Store(true) },
		"expired": func(_ *Daemon, a *v1.AgentConfigScanAuthority) {
			a.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
		},
		"unbounded": func(_ *Daemon, a *v1.AgentConfigScanAuthority) {
			a.LeaseExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			d := scanDaemon(t)
			a := *validScanCommand().AgentConfigScan
			change(d, &a)
			if _, _, err := d.beginAgentConfigScan(context.Background(), a); err == nil {
				t.Fatal("ineligible scan admitted")
			}
		})
	}
	d := scanDaemon(t)
	a := *validScanCommand().AgentConfigScan
	ctx, finish, err := d.beginAgentConfigScan(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.beginAgentConfigScan(context.Background(), a); err == nil {
		t.Fatal("parallel scan admitted")
	}
	d.stopAgentConfigScan(errAgentConfigScanStopped)
	if !errors.Is(context.Cause(ctx), errAgentConfigScanStopped) {
		t.Fatal("scan not cancelled")
	}
	finish()
	_, finish, err = d.beginAgentConfigScan(context.Background(), a)
	if err != nil {
		t.Fatal("reservation not released")
	}
	finish()
}
func TestAgentConfigScanDurableEventSurvivesOutcomeCommitFailure(t *testing.T) {
	d := scanDaemon(t)
	var calls atomic.Int32
	d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		calls.Add(1)
		return strings.Repeat("a", 40), scanner.Inventory{Items: []v1.AgentConfigDiscoveryItem{}}, nil
	}
	d.eventSink = func(context.Context, any) error { return errors.New("simulated delivery crash after durable append") }
	c := validScanCommand()
	env := scanEnvelope(c)
	_ = d.OnCommand(context.Background(), env)
	d.agentConfigScanCommands.Wait()
	before := scanResult(t, d, env.ID)
	d.eventSink = nil
	_ = d.OnCommand(context.Background(), env)
	d.agentConfigScanCommands.Wait()
	after := scanResult(t, d, env.ID)
	if calls.Load() != 1 || !reflect.DeepEqual(before, after) {
		t.Fatal("recovery changed first durable outcome")
	}
}
func TestAgentConfigScanCancellationAndLateSuccess(t *testing.T) {
	for _, code := range []string{"interrupted", "deadline_exceeded"} {
		t.Run(code, func(t *testing.T) {
			d := scanDaemon(t)
			ctx, cancel := context.WithCancel(context.Background())
			if code == "deadline_exceeded" {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
				return strings.Repeat("a", 40), scanner.Inventory{}, nil
			}
			if got := d.scanAgentConfiguration(ctx, *validScanCommand().AgentConfigScan, nil); got.Code != code || got.Scan != nil {
				t.Fatalf("late success=%+v", got)
			}
		})
	}
	a := *validScanCommand().AgentConfigScan
	a.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Truncate(time.Second).Format(time.RFC3339)
	event := scanResultEvent(a, v1.AgentConfigScanCommandMessageID(a.RequestID), v1.AgentConfigScanOutcome{Kind: "discovered"})
	if event.Outcome.Code != "deadline_exceeded" || event.ObservedAt != a.LeaseExpiresAt {
		t.Fatal("late result escaped deadline")
	}
}
func TestAgentConfigScanPauseAndExternalPauseCancellation(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "external"}[external], func(t *testing.T) {
			d := scanDaemon(t)
			if err := os.Chmod(d.cfg.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			scanCtx, finish, err := d.beginAgentConfigScan(context.Background(), *validScanCommand().AgentConfigScan)
			if err != nil {
				t.Fatal(err)
			}
			defer finish()
			if external {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go d.pauseStateLoop(ctx)
				if err := SetPausedState(d.cfg.StateDir, true); err != nil {
					t.Fatal(err)
				}
			} else if err := d.Pause(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-scanCtx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("pause did not cancel scan")
			}
		})
	}
}
func TestAgentConfigScanCapabilityExcludesUnsafeRegistrations(t *testing.T) {
	d := scanDaemon(t)
	d.cfg.Repositories = map[string]config.RepositoryConfig{"z": {URL: "ssh://git@example.invalid/repo"}, "a": {URL: "https://example.invalid/repo"}, "local": {URL: "/tmp/repo"}, "secret": {URL: "https://user:password@example.invalid/repo"}, "query": {URL: "https://example.invalid/repo?token=private"}, "bad/id": {URL: "https://example.invalid/repo"}}
	if got := d.agentConfigScanRepositories(); !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("advertised=%v", got)
	}
	d.cfg.Repositories = map[string]config.RepositoryConfig{}
	for i := 0; i < 257; i++ {
		d.cfg.Repositories[strings.Repeat("x", i+1)] = config.RepositoryConfig{URL: "https://example.invalid/repo"}
	}
	if len(d.agentConfigScanRepositories()) != 0 {
		t.Fatal("unbounded registration set advertised")
	}
}

func TestAgentConfigScanStopBeforeFinalizationDiscardsSuccess(t *testing.T) {
	d := scanDaemon(t)
	ctx, finish, err := d.beginAgentConfigScan(context.Background(), *validScanCommand().AgentConfigScan)
	if err != nil {
		t.Fatal(err)
	}
	d.stopAgentConfigScan(errAgentConfigScanStopped)
	outcome := d.finalizeAgentConfigScan(ctx, v1.AgentConfigScanOutcome{Kind: "discovered"})
	finish()
	if outcome.Code != "stopped" || outcome.Scan != nil {
		t.Fatalf("late success=%+v", outcome)
	}
}

func TestAgentConfigScanStoredResultCorruptionFailsClosed(t *testing.T) {
	for _, damage := range []string{"kind", "json", "identity", "authority", "outcome"} {
		t.Run(damage, func(t *testing.T) {
			d := scanDaemon(t)
			a := *validScanCommand().AgentConfigScan
			id := v1.AgentConfigScanCommandMessageID(a.RequestID)
			event := scanResultEvent(a, id, scanFailure("interrupted"))
			env := v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: v1.AgentConfigScanResultEventID(id), RunnerID: d.runnerID, SentAt: time.Now(), Payload: event}
			kind := journal.KindEvent
			switch damage {
			case "kind":
				kind = journal.KindCommand
			case "identity":
				env.RunnerID = "another-runner"
			case "authority":
				event.AttemptID = "other"
				event.CommandDigest = v1.AgentConfigScanCommandDigest(event.AgentConfigScanAuthority)
				env.Payload = event
			case "outcome":
				event.Outcome.Code = "uncontrolled"
				env.Payload = event
			}
			raw, _ := json.Marshal(env)
			if damage == "json" {
				raw = []byte("invalid")
			}
			if err := d.journal.Append(context.Background(), journal.Record{ID: env.ID, Kind: kind, Payload: raw, CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if found, err := d.hasAgentConfigScanResult(context.Background(), id, a); err == nil || found {
				t.Fatal("corrupt stored result accepted")
			}
		})
	}
}
func TestAgentConfigScanRejectsCommandIdentityAndPayloadBeforeGit(t *testing.T) {
	for _, damage := range []string{"identity", "mixed"} {
		t.Run(damage, func(t *testing.T) {
			d := scanDaemon(t)
			var calls atomic.Int32
			d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
				calls.Add(1)
				return "", scanner.Inventory{}, nil
			}
			c := validScanCommand()
			env := scanEnvelope(c)
			if damage == "identity" {
				env.ID = "wrong-command-id"
			} else {
				c.JobID = "job-1"
				env.Payload = c
			}
			if err := d.OnCommand(context.Background(), env); err != nil {
				t.Fatal(err)
			}
			d.agentConfigScanCommands.Wait()
			if calls.Load() != 0 {
				t.Fatal("invalid authority reached Git")
			}
			raw, _ := json.Marshal(env)
			digest, _ := commandContentDigest(env.Payload)
			receipt, _, err := d.journal.ReceiveCommand(context.Background(), journal.Record{ID: env.ID, Kind: journal.KindCommand, Payload: raw, CreatedAt: time.Now()}, digest)
			if err != nil || receipt.AckStatus != "rejected" || receipt.AckReasonCode != v1.CommandAckReasonInvalidCommand {
				t.Fatalf("receipt=%+v %v", receipt, err)
			}
		})
	}
}
func TestAgentConfigScanDefaultReaderNeverAllowsLocalRepository(t *testing.T) {
	d := scanDaemon(t)
	d.cfg.AllowUnsafeDirect = true
	d.cfg.Repositories["repository-1"] = config.RepositoryConfig{URL: t.TempDir()}
	outcome := d.scanAgentConfiguration(context.Background(), *validScanCommand().AgentConfigScan, nil)
	if outcome.Code != "repository_unavailable" {
		t.Fatalf("unsafe local registration=%+v", outcome)
	}
}
func TestAgentConfigScanFinalizationCancellationAndReservationOwnership(t *testing.T) {
	for _, code := range []string{"deadline_exceeded", "interrupted"} {
		t.Run(code, func(t *testing.T) {
			d := scanDaemon(t)
			ctx, cancel := context.WithCancel(context.Background())
			if code == "deadline_exceeded" {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			if got := d.finalizeAgentConfigScan(ctx, v1.AgentConfigScanOutcome{Kind: "discovered"}); got.Code != code {
				t.Fatalf("outcome=%+v", got)
			}
		})
	}
	d := scanDaemon(t)
	a := *validScanCommand().AgentConfigScan
	ctx, finish, err := d.beginAgentConfigScan(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	d.finalizeAgentConfigScan(ctx, scanFailure("scan_failed"))
	next, finishNext, err := d.beginAgentConfigScan(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	d.stopAgentConfigScan(errAgentConfigScanStopped)
	if !errors.Is(context.Cause(next), errAgentConfigScanStopped) {
		t.Fatal("old cleanup cleared new reservation")
	}
	finishNext()
}

func TestAgentConfigScanUnsupportedPlatformRejectsForgedCommand(t *testing.T) {
	d := scanDaemon(t)
	d.agentConfigScanPlatform = "windows"
	var calls atomic.Int32
	d.agentConfigScanReader = func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
		calls.Add(1)
		return "", scanner.Inventory{}, nil
	}
	c := validScanCommand()
	env := scanEnvelope(c)
	_ = d.OnCommand(context.Background(), env)
	d.agentConfigScanCommands.Wait()
	raw, _ := json.Marshal(env)
	digest, _ := commandContentDigest(c)
	receipt, _, err := d.journal.ReceiveCommand(context.Background(), journal.Record{ID: env.ID, Kind: journal.KindCommand, Payload: raw, CreatedAt: time.Now()}, digest)
	if err != nil || receipt.AckReasonCode != v1.CommandAckReasonUnsupportedCommand || calls.Load() != 0 {
		t.Fatalf("unsupported platform executed: %+v %v", receipt, err)
	}
}

func TestAgentConfigScanStrictWireRejectsEvenZeroValuedForeignFields(t *testing.T) {
	c := validScanCommand()
	raw, _ := json.Marshal(c)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	for key, value := range map[string]any{"job": nil, "job_id": "", "fencing_token": float64(0), "lease_expires_at": nil} {
		t.Run(key, func(t *testing.T) {
			candidate := map[string]any{}
			for k, v := range payload {
				candidate[k] = v
			}
			candidate[key] = value
			if _, err := decodeCommandPayload(candidate); err == nil {
				t.Fatal("foreign zero-valued field accepted")
			}
		})
	}
}
