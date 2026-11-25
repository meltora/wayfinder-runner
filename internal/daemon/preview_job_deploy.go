package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/mcpbridge"
)

const (
	previewJobDeployCodeCommandTimeout = "command_timeout"
	previewJobDeployCodeDeployTimeout  = "deploy_timeout"
	previewJobDeployCodeJobCancelled   = "job_cancelled"
	previewJobDeployCodeLeaseExpired   = "lease_expired"
	previewJobDeployCapMargin          = 30 * time.Second
)

var errPreviewJobDeployInFlight = errors.New("preview job deploy request is already in flight")

type previewJobDeployOutcome struct {
	Status          string
	Code            string
	EdgeHost        string
	ExpiresAt       time.Time
	CommitSet       map[string]string
	CommitSetDigest string
	Sequence        uint64
}

type previewJobDeployWaiter struct {
	key         string
	eventID     string
	attemptID   string
	manifestID  string
	digest      string
	sequence    uint64
	commitSet   map[string]string
	arrived     chan struct{}
	arriveOnce  sync.Once
	done        chan previewJobDeployOutcome
	resolveOnce sync.Once
}

func newPreviewJobDeployWaiter(
	attemptID, manifestID, requestID string,
	sequence uint64,
	commitSet map[string]string,
	digest string,
) *previewJobDeployWaiter {
	copied := make(map[string]string, len(commitSet))
	for repositoryID, commitSHA := range commitSet {
		copied[repositoryID] = commitSHA
	}
	return &previewJobDeployWaiter{
		key:        v1.PreviewJobDeployIdempotencyKey(requestID),
		eventID:    v1.PreviewDeployRequestedEventID(requestID),
		attemptID:  attemptID,
		manifestID: manifestID,
		digest:     digest,
		sequence:   sequence,
		commitSet:  copied,
		arrived:    make(chan struct{}),
		done:       make(chan previewJobDeployOutcome, 1),
	}
}

func (w *previewJobDeployWaiter) outcome(status, code string) previewJobDeployOutcome {
	commitSet := make(map[string]string, len(w.commitSet))
	for repositoryID, commitSHA := range w.commitSet {
		commitSet[repositoryID] = commitSHA
	}
	return previewJobDeployOutcome{
		Status: status, Code: code, CommitSet: commitSet, CommitSetDigest: w.digest, Sequence: w.sequence,
	}
}

func (w *previewJobDeployWaiter) resolve(outcome previewJobDeployOutcome) {
	w.resolveOnce.Do(func() { w.done <- outcome })
}

func (w *previewJobDeployWaiter) markArrived() {
	w.arriveOnce.Do(func() { close(w.arrived) })
}

func (w *previewJobDeployWaiter) hasArrived() bool {
	select {
	case <-w.arrived:
		return true
	default:
		return false
	}
}

type previewJobDeployRegistry struct {
	mu      sync.Mutex
	byKey   map[string]*previewJobDeployWaiter
	byEvent map[string]*previewJobDeployWaiter
}

func (r *previewJobDeployRegistry) register(w *previewJobDeployWaiter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKey == nil {
		r.byKey = make(map[string]*previewJobDeployWaiter)
		r.byEvent = make(map[string]*previewJobDeployWaiter)
	}
	if _, exists := r.byKey[w.key]; exists {
		return errPreviewJobDeployInFlight
	}
	for _, open := range r.byKey {
		if open.attemptID == w.attemptID {
			return errPreviewJobDeployInFlight
		}
	}
	r.byKey[w.key] = w
	r.byEvent[w.eventID] = w
	return nil
}

func (r *previewJobDeployRegistry) release(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, found := r.byKey[key]
	if !found {
		return
	}
	delete(r.byKey, key)
	delete(r.byEvent, w.eventID)
}

func (r *previewJobDeployRegistry) releaseAttempt(attemptID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, w := range r.byKey {
		if w.attemptID != attemptID {
			continue
		}
		w.resolve(w.outcome(v1.PreviewJobDeployStatusFailed, previewJobDeployCodeJobCancelled))
		delete(r.byKey, key)
		delete(r.byEvent, w.eventID)
	}
}

func (r *previewJobDeployRegistry) forKey(key string) *previewJobDeployWaiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byKey[key]
}

func (r *previewJobDeployRegistry) forEvent(eventID string) *previewJobDeployWaiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byEvent[eventID]
}

func (d *Daemon) previewJobDeployCommandArrived(operation v1.PreviewDeployOperation) bool {
	w := d.previewJobDeploys.forKey(operation.IdempotencyKey)
	if w == nil {
		return false
	}
	if operation.ExecutionManifestID != w.manifestID || operation.CommitSetDigest != w.digest {
		d.logger.Warn("preview deploy command matches a job request key but not its authority",
			"idempotency_key", operation.IdempotencyKey)
		return false
	}
	w.markArrived()
	return true
}

func (d *Daemon) previewJobDeployResultSent(event v1.PreviewDeployResultEvent) {
	w := d.previewJobDeploys.forKey(event.IdempotencyKey)
	if w == nil || event.ExecutionManifestID != w.manifestID || event.CommitSetDigest != w.digest {
		return
	}
	switch event.Outcome.Kind {
	case v1.PreviewDeployDeployed:
		outcome := w.outcome(v1.PreviewJobDeployStatusDeployed, "")
		outcome.EdgeHost = event.EdgeHost
		outcome.ExpiresAt = event.ExpiresAt
		w.resolve(outcome)
	case v1.PreviewDeployFailed:
		code := string(event.Outcome.Phase)
		if code == "" {
			code = string(event.Outcome.Kind)
		}
		w.resolve(w.outcome(v1.PreviewJobDeployStatusFailed, code))
	default:
		w.resolve(w.outcome(v1.PreviewJobDeployStatusFailed, string(event.Outcome.Kind)))
	}
}

func (d *Daemon) previewJobDeployCommandRejected(key, reason string) {
	w := d.previewJobDeploys.forKey(key)
	if w == nil || !w.hasArrived() {
		return
	}
	w.resolve(w.outcome(v1.PreviewJobDeployStatusFailed, reason))
}

func (d *Daemon) previewJobDeployCommandFinished(key string, err error) {
	var rejection *commandRejection
	if errors.As(err, &rejection) {
		d.previewJobDeployCommandRejected(key, string(rejection.reasonCode))
	}
}

func (d *Daemon) previewJobDeployEventRejected(eventID, code string) bool {
	w := d.previewJobDeploys.forEvent(eventID)
	if w == nil {
		return false
	}
	reply, final := v1.PreviewJobDeployRefusalCode(code)
	if !final {
		return false
	}
	w.resolve(w.outcome(v1.PreviewJobDeployStatusRefused, reply))
	return true
}

func previewJobDeployWaitCap() time.Duration {
	return mcpbridge.ToolCallTimeout(v1.PreviewJobDeployTool) - previewJobDeployCapMargin
}

func (jr *jobRunner) previewJobDeployLeaseTimer(
	clock func() time.Time,
	after func(time.Duration) <-chan time.Time,
) (<-chan time.Time, bool) {
	current := jr.d.leases.Current()
	if current == nil || current.AttemptID != jr.job.AttemptID || current.FencingToken != jr.job.FencingToken {
		return nil, true
	}
	remaining := current.ExpiresAt.Sub(clock())
	if remaining <= 0 {
		return nil, true
	}
	return after(remaining), false
}

func (jr *jobRunner) awaitPreviewJobDeploy(
	ctx context.Context,
	w *previewJobDeployWaiter,
	sentAt time.Time,
	clock func() time.Time,
	after func(time.Duration) <-chan time.Time,
) previewJobDeployOutcome {
	defer jr.d.previewJobDeploys.release(w.key)
	end := func(status, code string) previewJobDeployOutcome {
		w.resolve(w.outcome(status, code))
		return <-w.done
	}
	commandTimer := after(max(sentAt.Add(v1.PreviewJobDeployCommandTimeout).Sub(clock()), 0))
	capTimer := after(max(sentAt.Add(previewJobDeployWaitCap()).Sub(clock()), 0))
	leaseTimer, expired := jr.previewJobDeployLeaseTimer(clock, after)
	if expired {
		return end(v1.PreviewJobDeployStatusFailed, previewJobDeployCodeLeaseExpired)
	}
	arrived := w.arrived
	for {
		select {
		case outcome := <-w.done:
			return outcome
		case <-arrived:
			arrived = nil
			commandTimer = nil
		case <-commandTimer:
			commandTimer = nil
			if !w.hasArrived() {
				return end(v1.PreviewJobDeployStatusTimeout, previewJobDeployCodeCommandTimeout)
			}
		case <-leaseTimer:
			if leaseTimer, expired = jr.previewJobDeployLeaseTimer(clock, after); expired {
				return end(v1.PreviewJobDeployStatusFailed, previewJobDeployCodeLeaseExpired)
			}
		case <-capTimer:
			if !w.hasArrived() {
				return end(v1.PreviewJobDeployStatusTimeout, previewJobDeployCodeCommandTimeout)
			}
			return end(v1.PreviewJobDeployStatusFailed, previewJobDeployCodeDeployTimeout)
		case <-ctx.Done():
			return end(v1.PreviewJobDeployStatusFailed, previewJobDeployCodeJobCancelled)
		}
	}
}
