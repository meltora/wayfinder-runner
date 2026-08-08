package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (c *Coordinator) statePath() string { return filepath.Join(c.cfg.StateDir, "state.json") }
func (c *Coordinator) loadState() (durableState, error) {
	f, err := os.Open(c.statePath())
	if os.IsNotExist(err) {
		return durableState{Schema: 1, Phase: PhaseIdle}, nil
	}
	if err != nil {
		return durableState{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return durableState{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return durableState{}, fmt.Errorf("invalid updater state file")
	}
	var s durableState
	dec := json.NewDecoder(io.LimitReader(f, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("decode updater state: %w", err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return s, fmt.Errorf("updater state has trailing data")
	}
	if s.Schema != 1 || !validPhase(s.Phase) {
		return s, fmt.Errorf("invalid updater state")
	}
	if err := c.validateState(s); err != nil {
		return s, err
	}
	return s, nil
}

func (c *Coordinator) validateState(s durableState) error {
	if len(s.LastError) > maxPersistedErrorBytes {
		return fmt.Errorf("updater state error exceeds limit")
	}
	if s.Sequence > 0 && (!safeComponent(s.ReleaseID) || !validDigest(s.PayloadDigest)) {
		return fmt.Errorf("invalid installed release state")
	}
	if s.ObservedSequence > 0 && (!safeComponent(s.ObservedReleaseID) || !validDigest(s.ObservedDigest)) {
		return fmt.Errorf("invalid observed release state")
	}
	failedCount := 0
	if s.FailedSequence > 0 {
		failedCount++
	}
	if s.FailedDigest != "" {
		failedCount++
	}
	if s.FailedReason != "" {
		failedCount++
	}
	if s.FailedReleaseID != "" {
		failedCount++
	}
	if failedCount != 0 && failedCount != 4 {
		return fmt.Errorf("incomplete failed release state")
	}
	if failedCount == 4 && (!validDigest(s.FailedDigest) || !safeComponent(s.FailedReleaseID) || len(s.FailedReason) > maxPersistedErrorBytes) {
		return fmt.Errorf("invalid failed release state")
	}
	if s.ConsecutiveFailures > 32 {
		return fmt.Errorf("invalid failure counter")
	}
	if s.ConsecutiveFailures > 0 && s.LastPrepareAttempt.IsZero() {
		return fmt.Errorf("failure counter requires attempt time")
	}
	for _, p := range []string{s.PendingPath, s.PreviousTarget} {
		if p != "" && (filepath.Dir(filepath.Clean(p)) != c.cfg.VersionsDir || !safeComponent(filepath.Base(p))) {
			return fmt.Errorf("state contains path outside versions directory")
		}
		if p != "" {
			if err := c.validateSlotPath(p); err != nil {
				return err
			}
		}
	}
	if s.UpdateID != "" && !safeComponent(s.UpdateID) {
		return fmt.Errorf("invalid update id in state")
	}
	if s.PendingVersion != "" && !safeComponent(s.PendingVersion) {
		return fmt.Errorf("invalid pending version in state")
	}
	pendingMeta := s.UpdateID != "" && s.PendingSequence > 0 && validDigest(s.PendingDigest) && s.PendingVersion != ""
	switch s.Phase {
	case PhaseIdle:
		if s.UpdateID != "" || s.PendingSequence != 0 || s.PendingDigest != "" || s.PendingVersion != "" || s.PendingPath != "" || s.PreviousTarget != "" || !s.HealthDeadline.IsZero() {
			return fmt.Errorf("idle state retains transaction fields")
		}
	case PhasePreparing:
		if !pendingMeta || s.PendingPath != "" || s.PreviousTarget != "" {
			return fmt.Errorf("invalid preparing state")
		}
	case PhasePrepared:
		if !pendingMeta || s.PendingPath == "" || s.PreviousTarget != "" {
			return fmt.Errorf("invalid prepared state")
		}
	case PhaseStopping, PhaseSwitching, PhaseStarting, PhaseRollingBack:
		if !pendingMeta || s.PendingPath == "" || s.PreviousTarget == "" {
			return fmt.Errorf("invalid activation state")
		}
	case PhaseAwaitingHealth:
		if !pendingMeta || s.PendingPath == "" || s.PreviousTarget == "" || s.HealthDeadline.IsZero() {
			return fmt.Errorf("invalid health state")
		}
	case PhaseCommittingUpdater:
		if !pendingMeta || s.PendingPath == "" || s.PreviousTarget == "" || s.Sequence != s.PendingSequence || s.ReleaseID != s.UpdateID {
			return fmt.Errorf("invalid updater commit state")
		}
	}
	return nil
}

func (c *Coordinator) validateSlotPath(path string) error {
	clean := filepath.Clean(path)
	if filepath.Dir(clean) != c.cfg.VersionsDir || !safeComponent(filepath.Base(clean)) {
		return fmt.Errorf("slot path is outside versions directory")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("slot is not a protected real directory")
	}
	if c.cfg.RequireRootOwnership && !ownedByRoot(info) {
		return fmt.Errorf("slot is not root owned")
	}
	for _, name := range requiredBinaries {
		entry, err := os.Lstat(filepath.Join(clean, name))
		if err != nil {
			return err
		}
		if !entry.Mode().IsRegular() || entry.Mode().Perm()&0o022 != 0 || entry.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("slot binary %s is unsafe", name)
		}
		if c.cfg.RequireRootOwnership && !ownedByRoot(entry) {
			return fmt.Errorf("slot binary is not root owned")
		}
	}
	return nil
}
func (c *Coordinator) saveState(s durableState) error {
	if err := c.validateState(s); err != nil {
		return fmt.Errorf("refuse invalid updater state: %w", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	// #nosec G304 -- StateDir is absolute, private, and root-owned; the random
	// filename is generated by os.CreateTemp.
	tmp, err := os.CreateTemp(c.cfg.StateDir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(b); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, c.statePath()); err != nil {
		return err
	}
	return syncDir(c.cfg.StateDir)
}
func validPhase(p Phase) bool {
	switch p {
	case PhaseIdle, PhasePreparing, PhasePrepared, PhaseStopping, PhaseSwitching, PhaseStarting, PhaseAwaitingHealth, PhaseRollingBack, PhaseCommittingUpdater:
		return true
	}
	return false
}
func syncDir(path string) error {
	// #nosec G304 -- callers pass only validated root-owned state, versions, or
	// installation directories; opening them is required for durable fsync.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
