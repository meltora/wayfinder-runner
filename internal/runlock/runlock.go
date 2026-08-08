package runlock

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type Lock struct {
	file *os.File
}

func Acquire(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	stateInfo, err := os.Lstat(stateDir)
	if err != nil {
		return nil, err
	}
	if !stateInfo.IsDir() || stateInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("runner state directory must be a real directory")
	}
	if stateInfo.Mode().Perm()&0o077 != 0 {
		// #nosec G302 -- runner state is a private directory; owner traversal requires 0700.
		if err := os.Chmod(stateDir, 0o700); err != nil {
			return nil, fmt.Errorf("secure runner state directory: %w", err)
		}
	}
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Lstat("runner.lock"); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("runner lock path is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := root.OpenFile("runner.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another runner daemon is already using %s: %w", stateDir, err)
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
