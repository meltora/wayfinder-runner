package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/validation"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"time"
)

var errAgentConfigScanStopped = errors.New("agent configuration scan stopped")

type agentConfigScanReadFunc func(context.Context, string, string, *repo.GitHTTPSCredential) (string, scanner.Inventory, error)
type agentConfigScanState struct {
	agentConfigScanPlatform      string
	agentConfigScanReader        agentConfigScanReadFunc
	agentConfigScanCommands      sync.WaitGroup
	agentConfigScanCommandMu     sync.Mutex
	agentConfigScanCommandActive map[string]struct{}
	// The active cancellation is protected by Daemon.jobMu, shared with jobs.
	agentConfigScanCancel  context.CancelCauseFunc
	agentConfigScanContext context.Context
}

func validateAgentConfigScanCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdAgentConfigScan {
		if command.AgentConfigScan != nil || command.ContentCapture != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.AgentConfigScan == nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	fields := reflect.ValueOf(command)
	schema := fields.Type()
	for i := 0; i < fields.NumField(); i++ {
		name := schema.Field(i).Name
		if name != "Command" && name != "AgentConfigScan" && name != "ContentCapture" && !fields.Field(i).IsZero() {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
	}
	if err := validation.AgentConfigScanAuthority(*command.AgentConfigScan); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if command.ContentCapture != nil {
		if err := validation.AgentConfigContentCapture(*command.ContentCapture); err != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
		}
	}
	return nil
}
func (d *Daemon) agentConfigScanCommandIsActive(id string) bool {
	d.agentConfigScanCommandMu.Lock()
	defer d.agentConfigScanCommandMu.Unlock()
	_, ok := d.agentConfigScanCommandActive[id]
	return ok
}
func (d *Daemon) agentConfigScanCommandStart(id string) {
	d.agentConfigScanCommandMu.Lock()
	defer d.agentConfigScanCommandMu.Unlock()
	if d.agentConfigScanCommandActive == nil {
		d.agentConfigScanCommandActive = map[string]struct{}{}
	}
	d.agentConfigScanCommandActive[id] = struct{}{}
	d.agentConfigScanCommands.Add(1)
}
func (d *Daemon) agentConfigScanCommandFinish(id string) {
	d.agentConfigScanCommandMu.Lock()
	delete(d.agentConfigScanCommandActive, id)
	d.agentConfigScanCommandMu.Unlock()
	d.agentConfigScanCommands.Done()
}
func (d *Daemon) stopAgentConfigScan(cause error) {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.agentConfigScanCancel != nil {
		d.agentConfigScanCancel(cause)
	}
}
func (d *Daemon) beginAgentConfigScan(ctx context.Context, a v1.AgentConfigScanAuthority) (context.Context, func(), error) {
	platform := d.agentConfigScanPlatform
	if platform == "" {
		platform = runtime.GOOS
	}
	if !repo.AgentConfigScanSupportedOS(platform) {
		return nil, nil, rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	deadline, _ := validation.AgentConfigScanTimestamp(a.LeaseExpiresAt)
	now := time.Now()
	if !deadline.After(now) || deadline.After(now.Add(10*time.Minute)) {
		return nil, nil, rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.Paused() {
		return nil, nil, rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
	}
	if d.updateDrain != nil {
		return nil, nil, rejectCommand(v1.CommandAckReasonRunnerDraining, nil)
	}
	if d.activeJob != nil || d.workIntakeProposalActive || d.agentConfigScanCancel != nil || d.recoveredLease.Load() || d.leases.Current() != nil {
		return nil, nil, rejectCommand(v1.CommandAckReasonRunnerBusy, nil)
	}
	bounded, deadlineCancel := context.WithDeadline(ctx, deadline)
	scanCtx, cancel := context.WithCancelCause(bounded)
	d.agentConfigScanCancel = cancel
	d.agentConfigScanContext = scanCtx
	finish := func() {
		cancel(nil)
		deadlineCancel()
		d.jobMu.Lock()
		if d.agentConfigScanContext == scanCtx {
			d.agentConfigScanCancel = nil
			d.agentConfigScanContext = nil
		}
		d.jobMu.Unlock()
	}
	return scanCtx, finish, nil
}
func (d *Daemon) scanAgentConfiguration(ctx context.Context, a v1.AgentConfigScanAuthority, providerCredential *v1.ProviderCredential, capture *v1.AgentConfigContentCapture) v1.AgentConfigScanOutcome {
	configuration := d.hotConfiguration()
	repository, ok := configuration.repositories[a.RepositoryID]
	if !ok {
		return scanFailure("repository_unavailable")
	}
	credential, gitHubOrigin, credentialErr := repositoryCredential(providerCredential, repository.URL, configuration.sourcePublicationInstallations, time.Now())
	if credentialErr != nil {
		return scanFailure("repository_credential_unavailable")
	}
	read := d.agentConfigScanReader
	if read == nil {
		manager := repo.NewManager(filepath.Join(d.cfg.StateDir, "repos"))
		options := repo.AgentConfigScanOptions{}
		if capture != nil {
			options.ContentMaxItemBytes = v1.AgentConfigContentMaxItemBytes
		}
		read = func(ctx context.Context, url, branch string, credential *repo.GitHTTPSCredential) (string, scanner.Inventory, error) {
			return manager.ScanAgentConfigWithOptions(ctx, url, branch, credential, options)
		}
	}
	sha, inventory, err := read(ctx, repository.URL, a.BranchRef, credential)
	switch {
	case errors.Is(context.Cause(ctx), errAgentConfigScanStopped):
		return scanFailure("stopped")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return scanFailure("deadline_exceeded")
	case ctx.Err() != nil:
		return scanFailure("interrupted")
	case errors.Is(err, repo.ErrScanLimit):
		return scanFailure("limit_exceeded")
	case errors.Is(err, repo.ErrScanInvalidInput):
		return scanFailure("repository_unavailable")
	case errors.Is(err, scanner.ErrIncompleteScan):
		return scanFailure("scan_failed")
	case gitHubOrigin && errors.Is(err, repo.ErrRepositoryCredentialUnavailable):
		return scanFailure("repository_credential_unavailable")
	case gitHubOrigin && errors.Is(err, repo.ErrRepositoryAccessDenied):
		return scanFailure("repository_access_denied")
	case err != nil:
		return scanFailure("checkout_failed")
	}
	observed := time.Now().UTC().Truncate(time.Second)
	scan, err := scanner.BuildDiscoveryEvent(scanner.DiscoveryInput{ProjectID: a.ProjectID, RepositoryID: a.RepositoryID, BranchRef: a.BranchRef, CommitSHA: sha, ObservedAt: observed}, inventory)
	if err != nil || validation.AgentConfigDiscoveryEvent(scan) != nil {
		return scanFailure("scan_failed")
	}
	return withItemContents(v1.AgentConfigScanOutcome{Kind: "discovered", Scan: &scan}, inventory, capture)
}

const (
	maxScanResultEnvelopeBytes = 2 << 20
	scanResultEnvelopeReserve  = 16 << 10
)

func withItemContents(outcome v1.AgentConfigScanOutcome, inventory scanner.Inventory, capture *v1.AgentConfigContentCapture) v1.AgentConfigScanOutcome {
	if capture == nil || outcome.Scan == nil || len(inventory.Contents) == 0 {
		return outcome
	}
	metadata, err := json.Marshal(outcome)
	if err != nil {
		return outcome
	}
	limits := scanner.ContentLimits{
		MaxItemBytes:    v1.AgentConfigContentMaxItemBytes,
		MaxTotalBytes:   v1.AgentConfigContentMaxTotalBytes,
		MaxItems:        v1.AgentConfigContentMaxItems,
		MaxEncodedBytes: maxScanResultEnvelopeBytes - scanResultEnvelopeReserve - len(metadata),
	}
	outcome.ItemContents = scanner.ItemContents(outcome.Scan.Items, inventory.Contents, limits)
	return outcome
}
func scanFailure(code string) v1.AgentConfigScanOutcome {
	return v1.AgentConfigScanOutcome{Kind: "failed", Code: code}
}
func scanResultEvent(a v1.AgentConfigScanAuthority, id string, outcome v1.AgentConfigScanOutcome) v1.AgentConfigScanResultEvent {
	observed := time.Now().UTC().Truncate(time.Second)
	deadline, _ := validation.AgentConfigScanTimestamp(a.LeaseExpiresAt)
	if observed.After(deadline) {
		observed = deadline
		outcome = scanFailure("deadline_exceeded")
	}
	return v1.AgentConfigScanResultEvent{Kind: v1.EventAgentConfigScanResult, AgentConfigScanAuthority: a, CommandMessageID: id, ObservedAt: observed.Format(time.RFC3339), Outcome: outcome}
}

// End the active phase under the same lock as stop/job admission. A stop that
// wins this lock must invalidate success; a later stop sees completed work.
func (d *Daemon) finalizeAgentConfigScan(ctx context.Context, outcome v1.AgentConfigScanOutcome) v1.AgentConfigScanOutcome {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	deadline, hasDeadline := ctx.Deadline()
	switch {
	case errors.Is(context.Cause(ctx), errAgentConfigScanStopped) || d.Paused():
		outcome = scanFailure("stopped")
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || (hasDeadline && !time.Now().Before(deadline)):
		outcome = scanFailure("deadline_exceeded")
	case ctx.Err() != nil:
		outcome = scanFailure("interrupted")
	}
	if d.agentConfigScanContext == ctx {
		d.agentConfigScanCancel = nil
		d.agentConfigScanContext = nil
	}
	return outcome
}

func validateAgentConfigScanWireFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	expected := 2
	if capture, present := fields["content_capture"]; present {
		if bytes.Equal(capture, []byte("null")) {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		expected = 3
	}
	if len(fields) != expected || fields["command"] == nil || fields["agent_config_scan"] == nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	return nil
}
