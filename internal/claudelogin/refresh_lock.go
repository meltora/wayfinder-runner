package claudelogin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const refreshLockFile = ".credentials.json.refresh.flock"

const refreshLockRetry = 100 * time.Millisecond

func LockRefresh(ctx context.Context, configDir string) (func() error, error) {
	file, err := os.OpenFile(filepath.Join(configDir, refreshLockFile), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open claude login refresh lock: %w", err)
	}
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = file.Close()
			return nil, fmt.Errorf("take claude login refresh lock: %w", err)
		}
		timer := time.NewTimer(refreshLockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ErrLocked
		case <-timer.C:
		}
	}
	return func() error {
		unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return errors.Join(unlockErr, file.Close())
	}, nil
}
