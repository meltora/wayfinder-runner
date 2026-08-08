package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func atomicSymlink(target, link string) error {
	dir := filepath.Dir(link)
	// #nosec G301 -- this root-owned executable slot tree must be traversable by
	// the unprivileged runner; write permission remains root-only.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".link-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	defer func() { _ = os.Remove(name) }()
	if err := os.Symlink(target, name); err != nil {
		return err
	}
	if err := os.Rename(name, link); err != nil {
		return err
	}
	return syncDir(dir)
}
func readOptionalLink(path string) (string, error) {
	v, err := os.Readlink(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read link %s: %w", path, err)
	}
	if !filepath.IsAbs(v) {
		v = filepath.Clean(filepath.Join(filepath.Dir(path), v))
	}
	return v, nil
}

func (c *Coordinator) abortBeforeSwitch(state durableState, cause error) error {
	if err := c.service.Start(context.Background(), c.cfg.RunnerService); err != nil {
		state.LastError = boundedError(fmt.Errorf("%v; restore runner: %v", cause, err))
		if persistErr := c.saveState(state); persistErr != nil {
			return fmt.Errorf("%v; restore runner: %v; persist recovery state: %w", cause, err, persistErr)
		}
		return fmt.Errorf("%v; restore runner: %w", cause, err)
	}
	state.Phase = PhaseIdle
	state.LastError = boundedError(cause)
	state.clearPending()
	if err := c.saveState(state); err != nil {
		return fmt.Errorf("%v; persist aborted activation: %w", cause, err)
	}
	return cause
}
func (c *Coordinator) rollback(ctx context.Context, state durableState, cause error) error {
	state.Phase = PhaseRollingBack
	state.LastError = boundedError(cause)
	state.FailedSequence = state.PendingSequence
	state.FailedDigest = state.PendingDigest
	state.FailedReason = boundedError(cause)
	state.FailedReleaseID = state.UpdateID
	if err := c.saveState(state); err != nil {
		return fmt.Errorf("%v; persist rollback: %w", cause, err)
	}
	if err := c.service.Stop(ctx, c.cfg.RunnerService); err != nil {
		return fmt.Errorf("%v; rollback stop: %w", cause, err)
	}
	if state.PreviousTarget == "" {
		return fmt.Errorf("%v; rollback unavailable: no previous version", cause)
	}
	if err := atomicSymlink(state.PreviousTarget, c.cfg.CurrentLink); err != nil {
		return fmt.Errorf("%v; rollback switch: %w", cause, err)
	}
	if err := c.service.Start(ctx, c.cfg.RunnerService); err != nil {
		return fmt.Errorf("%v; rollback restart: %w", cause, err)
	}
	state.Phase = PhaseIdle
	state.clearPending()
	if err := c.saveState(state); err != nil {
		return err
	}
	c.gc(state)
	return nil
}
func (c *Coordinator) rollbackFailure(ctx context.Context, state durableState, cause error) error {
	if err := c.rollback(ctx, state, cause); err != nil {
		return err
	}
	return cause
}
