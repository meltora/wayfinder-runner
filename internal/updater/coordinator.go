package updater

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Coordinator struct {
	cfg       Config
	catalog   Catalog
	artifacts ArtifactFetcher
	service   ServiceController
	preflight Preflighter
	clock     Clock
	disk      DiskSpace
	failpoint func(string) error
	mu        sync.Mutex
}

func New(cfg Config, deps Dependencies) (*Coordinator, error) {
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.RunnerVersion == "" {
		cfg.RunnerVersion = "0.1.0"
	}
	if cfg.UpdaterVersion == "" {
		cfg.UpdaterVersion = "0.1.0"
	}
	if !semanticVersionPattern.MatchString(cfg.RunnerVersion) || !semanticVersionPattern.MatchString(cfg.UpdaterVersion) {
		return nil, fmt.Errorf("runner and updater versions must be canonical semantic versions")
	}
	if cfg.Channel == "" || !safeComponent(cfg.Channel) {
		return nil, fmt.Errorf("valid channel is required")
	}
	for name, path := range map[string]string{"versions directory": cfg.VersionsDir, "state directory": cfg.StateDir, "current link": cfg.CurrentLink, "previous link": cfg.PreviousLink, "updater current link": cfg.UpdaterCurrentLink} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	if cfg.RunnerService == "" || strings.ContainsAny(cfg.RunnerService, "/\x00\n\r") {
		return nil, fmt.Errorf("valid runner service is required")
	}
	if cfg.HealthTimeout <= 0 {
		cfg.HealthTimeout = 90 * time.Second
	}
	if cfg.PrepareTimeout <= 0 || cfg.PrepareTimeout > 30*time.Minute {
		cfg.PrepareTimeout = 15 * time.Minute
	}
	if cfg.MaxArchiveBytes <= 0 {
		cfg.MaxArchiveBytes = 256 << 20
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 128 << 20
	}
	if cfg.MaxExtractedBytes <= 0 {
		cfg.MaxExtractedBytes = 2 * cfg.MaxFileBytes
	}
	if cfg.RetainVersions < 2 {
		cfg.RetainVersions = 3
	}
	if cfg.MinPrepareInterval < time.Minute {
		cfg.MinPrepareInterval = time.Minute
	}
	if cfg.MaxPrepareBackoff < cfg.MinPrepareInterval || cfg.MaxPrepareBackoff > 24*time.Hour {
		cfg.MaxPrepareBackoff = time.Hour
	}
	if deps.Catalog == nil || deps.Artifacts == nil || deps.Service == nil || deps.Preflight == nil {
		return nil, fmt.Errorf("catalog, artifact fetcher, service controller, and preflighter are required")
	}
	if deps.Clock == nil {
		deps.Clock = realClock{}
	}
	if deps.Disk == nil {
		deps.Disk = systemDiskSpace{}
	}
	// #nosec G301 -- signed executables must be traversable/readable by the
	// unprivileged runner; validation below requires root ownership and no
	// group/other write permission.
	if err := os.MkdirAll(cfg.VersionsDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	if cfg.RequireRootOwnership {
		if err := validateRootDirectory(cfg.VersionsDir, false); err != nil {
			return nil, err
		}
		if err := validateRootDirectory(cfg.StateDir, true); err != nil {
			return nil, err
		}
	}
	return &Coordinator{cfg: cfg, catalog: deps.Catalog, artifacts: deps.Artifacts, service: deps.Service, preflight: deps.Preflight, clock: deps.Clock, disk: deps.Disk, failpoint: deps.Failpoint}, nil
}

var semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func safeComponent(v string) bool {
	if v == "" || len(v) > 128 || v == "." || v == ".." {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (c *Coordinator) Prepare(ctx context.Context, requestedUpdateID string) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.processLock()
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.PrepareTimeout)
	defer cancel()
	state, err := c.loadState()
	if err != nil {
		return Status{}, err
	}
	if state.Phase != PhaseIdle {
		return statusFrom(state), fmt.Errorf("update transaction is %s", state.Phase)
	}
	if requestedUpdateID != "" && requestedUpdateID == state.ReleaseID {
		return statusFrom(state), ErrAlreadyInstalled
	}
	if requestedUpdateID != "" && requestedUpdateID == state.FailedReleaseID {
		return statusFrom(state), ErrSuppressed
	}
	now := c.clock.Now()
	backoff := c.cfg.MinPrepareInterval
	for i := uint32(0); i < state.ConsecutiveFailures && backoff < c.cfg.MaxPrepareBackoff; i++ {
		if backoff > c.cfg.MaxPrepareBackoff/2 {
			backoff = c.cfg.MaxPrepareBackoff
			break
		}
		backoff *= 2
	}
	if !state.LastPrepareAttempt.IsZero() && now.Before(state.LastPrepareAttempt.Add(backoff)) {
		return statusFrom(state), ErrRateLimited
	}
	state.LastPrepareAttempt = now
	if err := c.saveState(state); err != nil {
		return Status{}, err
	}
	runnerVersion, err := c.currentRunnerVersion()
	if err != nil {
		incrementFailures(&state)
		_ = c.saveState(state)
		return Status{}, fmt.Errorf("resolve active runner version: %w", err)
	}
	rel, err := c.catalog.Latest(ctx, CatalogQuery{Channel: c.cfg.Channel, OS: c.cfg.OS, Arch: c.cfg.Arch, RunnerVersion: runnerVersion, UpdaterVersion: c.cfg.UpdaterVersion, CurrentSequence: state.ObservedSequence, CurrentReleaseID: state.ObservedReleaseID, CurrentPayloadDigest: state.ObservedDigest})
	if err != nil {
		incrementFailures(&state)
		_ = c.saveState(state)
		return Status{}, fmt.Errorf("catalog: %w", err)
	}
	if err := c.validateRelease(rel, state); err != nil {
		incrementFailures(&state)
		_ = c.saveState(state)
		return Status{}, err
	}
	if requestedUpdateID == "" || requestedUpdateID != rel.ReleaseID {
		incrementFailures(&state)
		_ = c.saveState(state)
		return Status{}, ErrTransactionMismatch
	}
	state.Phase = PhasePreparing
	state.UpdateID = rel.ReleaseID
	state.PendingSequence = rel.Sequence
	state.PendingDigest = rel.PayloadDigest
	state.PendingVersion = rel.Version
	state.ObservedSequence = rel.Sequence
	state.ObservedReleaseID = rel.ReleaseID
	state.ObservedDigest = rel.PayloadDigest
	state.LastError = ""
	if err := c.saveState(state); err != nil {
		return Status{}, err
	}
	if err := c.hit("after_prepare_state"); err != nil {
		return Status{}, err
	}
	stage, err := c.stage(ctx, rel)
	if err != nil {
		state.Phase = PhaseIdle
		state.LastError = boundedError(err)
		incrementFailures(&state)
		state.clearPending()
		_ = c.saveState(state)
		return Status{}, err
	}
	state.PendingPath = stage
	state.Phase = PhasePrepared
	state.ConsecutiveFailures = 0
	if err := c.saveState(state); err != nil {
		return Status{}, err
	}
	c.gc(state)
	return statusFrom(state), nil
}

func (c *Coordinator) currentRunnerVersion() (string, error) {
	target, err := readOptionalLink(c.cfg.CurrentLink)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", fmt.Errorf("active runner slot is missing")
	}
	if err := c.validateSlotPath(target); err != nil {
		return "", fmt.Errorf("active runner slot: %w", err)
	}
	slot := filepath.Base(target)
	if semanticVersionPattern.MatchString(slot) {
		return slot, nil
	}
	// Automatic slots are named with a fixed-width sequence prefix so they are
	// sortable and collision resistant: 00000000000000000042-1.2.3.
	if len(slot) > 21 && slot[20] == '-' {
		for _, r := range slot[:20] {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("active slot has no canonical version")
			}
		}
		candidate := slot[21:]
		if semanticVersionPattern.MatchString(candidate) {
			return candidate, nil
		}
	}
	// The bootstrap installer historically used an unversioned initial slot;
	// only in that one explicit case is the compiled runner/updater version a
	// valid fallback because both binaries came from the same signed archive.
	if slot == "initial" {
		return c.cfg.RunnerVersion, nil
	}
	return "", fmt.Errorf("active slot has no canonical version")
}

func (c *Coordinator) Activate(ctx context.Context, updateID string) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.processLock()
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	state, err := c.loadState()
	if err != nil {
		return Status{}, err
	}
	if state.Phase != PhasePrepared {
		return Status{}, ErrNoPreparedUpdate
	}
	if updateID == "" || updateID != state.UpdateID {
		return Status{}, ErrTransactionMismatch
	}
	old, err := readOptionalLink(c.cfg.CurrentLink)
	if err != nil {
		return Status{}, err
	}
	if old == "" {
		return Status{}, fmt.Errorf("active runner slot is missing")
	}
	if err := c.validateSlotPath(old); err != nil {
		return Status{}, fmt.Errorf("active runner slot: %w", err)
	}
	if err := c.validateSlotPath(state.PendingPath); err != nil {
		return Status{}, fmt.Errorf("pending runner slot: %w", err)
	}
	state.PreviousTarget = old
	state.Phase = PhaseStopping
	if err := c.saveState(state); err != nil {
		return Status{}, err
	}
	if err := c.service.Stop(ctx, c.cfg.RunnerService); err != nil {
		return Status{}, c.abortBeforeSwitch(state, err)
	}
	if err := c.hit("after_stop"); err != nil {
		return Status{}, c.abortBeforeSwitch(state, err)
	}
	state.Phase = PhaseSwitching
	if err := c.saveState(state); err != nil {
		return Status{}, c.abortBeforeSwitch(state, err)
	}
	if old != "" {
		if err := atomicSymlink(old, c.cfg.PreviousLink); err != nil {
			return Status{}, c.abortBeforeSwitch(state, err)
		}
	}
	if err := atomicSymlink(state.PendingPath, c.cfg.CurrentLink); err != nil {
		return Status{}, c.abortBeforeSwitch(state, err)
	}
	if err := c.hit("after_switch"); err != nil {
		return Status{}, c.rollbackFailure(ctx, state, err)
	}
	state.Phase = PhaseStarting
	if err := c.saveState(state); err != nil {
		return Status{}, c.rollbackFailure(ctx, state, err)
	}
	if err := c.service.Start(ctx, c.cfg.RunnerService); err != nil {
		cause := fmt.Errorf("start updated runner: %w", err)
		return Status{}, c.rollbackFailure(ctx, state, cause)
	}
	state.HealthDeadline = c.clock.Now().Add(c.cfg.HealthTimeout)
	state.Phase = PhaseAwaitingHealth
	if err := c.saveState(state); err != nil {
		return Status{}, c.rollbackFailure(ctx, state, err)
	}
	return statusFrom(state), nil
}

func (c *Coordinator) Health(ctx context.Context, updateID, version string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.processLock()
	if err != nil {
		return err
	}
	defer unlock()
	state, err := c.loadState()
	if err != nil {
		return err
	}
	if state.Phase == PhaseCommittingUpdater && updateID == state.UpdateID && version == state.PendingVersion {
		if err := atomicSymlink(filepath.Join(state.PendingPath, "our-runner-updater"), c.cfg.UpdaterCurrentLink); err != nil {
			return err
		}
		return c.finishUpdaterCommit(state)
	}
	if state.Phase != PhaseAwaitingHealth || updateID != state.UpdateID || version != state.PendingVersion {
		return ErrTransactionMismatch
	}
	if !c.clock.Now().Before(state.HealthDeadline) {
		cause := fmt.Errorf("health deadline expired")
		return c.rollbackFailure(ctx, state, cause)
	}
	state.Phase = PhaseCommittingUpdater
	state.Sequence = state.PendingSequence
	state.PayloadDigest = state.PendingDigest
	state.Version = state.PendingVersion
	state.ReleaseID = state.UpdateID
	state.LastError = ""
	if err := c.saveState(state); err != nil {
		return err
	}
	if err := c.hit("after_health_commit"); err != nil {
		return err
	}
	if err := atomicSymlink(filepath.Join(state.PendingPath, "our-runner-updater"), c.cfg.UpdaterCurrentLink); err != nil {
		return err
	}
	if err := c.hit("after_updater_switch"); err != nil {
		return err
	}
	return c.finishUpdaterCommit(state)
}

func (c *Coordinator) Status(ctx context.Context) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.processLock()
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	state, err := c.loadState()
	if err != nil {
		return Status{}, err
	}
	if state.Phase == PhaseAwaitingHealth && !c.clock.Now().Before(state.HealthDeadline) {
		if err := c.rollback(ctx, state, fmt.Errorf("health deadline expired")); err != nil {
			return Status{}, err
		}
		state, err = c.loadState()
	}
	return statusFrom(state), err
}

func (c *Coordinator) Recover(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.processLock()
	if err != nil {
		return err
	}
	defer unlock()
	state, err := c.loadState()
	if err != nil {
		return err
	}
	switch state.Phase {
	case PhaseIdle, PhasePrepared:
		return nil
	case PhasePreparing:
		state.Phase = PhaseIdle
		state.LastError = "interrupted preparation"
		state.clearPending()
		return c.saveState(state)
	case PhaseAwaitingHealth:
		if c.clock.Now().Before(state.HealthDeadline) {
			return nil
		}
		return c.rollback(ctx, state, fmt.Errorf("health deadline expired"))
	case PhaseCommittingUpdater:
		if err := atomicSymlink(filepath.Join(state.PendingPath, "our-runner-updater"), c.cfg.UpdaterCurrentLink); err != nil {
			return err
		}
		return c.finishUpdaterCommit(state)
	default:
		return c.rollback(ctx, state, fmt.Errorf("interrupted activation at phase %s", state.Phase))
	}
}
func (c *Coordinator) finishUpdaterCommit(state durableState) error {
	state.Phase = PhaseIdle
	state.clearPending()
	state.FailedSequence = 0
	state.FailedDigest = ""
	state.FailedReason = ""
	state.FailedReleaseID = ""
	if err := c.saveState(state); err != nil {
		return err
	}
	c.gc(state)
	return nil
}

func (c *Coordinator) validateRelease(r Release, s durableState) error {
	if !safeComponent(r.ReleaseID) || !safeComponent(r.Version) || r.Sequence == 0 || r.Channel != c.cfg.Channel || r.OS != c.cfg.OS || r.Arch != c.cfg.Arch || r.Size <= 0 || r.Size > c.cfg.MaxArchiveBytes || !validDigest(r.SHA256) || !validDigest(r.PayloadDigest) {
		return fmt.Errorf("catalog returned invalid release")
	}
	if r.Sequence < s.ObservedSequence {
		return fmt.Errorf("release sequence rollback")
	}
	if r.Sequence == s.FailedSequence && r.PayloadDigest == s.FailedDigest {
		return ErrSuppressed
	}
	if r.Sequence == s.ObservedSequence {
		if r.PayloadDigest != s.ObservedDigest || r.ReleaseID != s.ObservedReleaseID {
			return ErrEquivocation
		}
		if r.Sequence == s.Sequence && r.PayloadDigest == s.PayloadDigest && r.ReleaseID == s.ReleaseID {
			return ErrAlreadyInstalled
		}
	}
	return nil
}
func validDigest(v string) bool {
	if len(v) != 64 || v != strings.ToLower(v) {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
func incrementFailures(s *durableState) {
	if s.ConsecutiveFailures < 32 {
		s.ConsecutiveFailures++
	}
}
func (c *Coordinator) hit(name string) error {
	if c.failpoint == nil {
		return nil
	}
	return c.failpoint(name)
}
func statusFrom(s durableState) Status {
	return Status{Phase: s.Phase, UpdateID: s.UpdateID, ReleaseID: s.ReleaseID, FailedReleaseID: s.FailedReleaseID, Version: first(s.PendingVersion, s.Version), Sequence: max(s.PendingSequence, s.Sequence), HealthDeadline: s.HealthDeadline, LastError: s.LastError}
}
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
func (s *durableState) clearPending() {
	s.UpdateID = ""
	s.PendingSequence = 0
	s.PendingDigest = ""
	s.PendingVersion = ""
	s.PendingPath = ""
	s.PreviousTarget = ""
	s.HealthDeadline = time.Time{}
}
