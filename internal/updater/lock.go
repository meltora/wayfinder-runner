package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (c *Coordinator) processLock() (func(), error) {
	path := filepath.Join(c.cfg.StateDir, "transaction.lock")
	// #nosec G304 -- StateDir is an absolute, private, root-owned directory
	// validated when the coordinator is created; the filename is fixed.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(fmt.Errorf("invalid updater lock file"), f.Close())
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("another updater transaction is active: %w", err), f.Close())
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
