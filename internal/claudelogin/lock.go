package claudelogin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrLocked = errors.New("claude login is locked by another process")

type LockOptions struct {
	Stale   time.Duration
	Update  time.Duration
	Retry   time.Duration
	Timeout time.Duration
	Now     func() time.Time
}

func (o LockOptions) withDefaults() LockOptions {
	if o.Stale <= 0 {
		o.Stale = 60 * time.Second
	}
	if o.Update <= 0 {
		o.Update = 5 * time.Second
	}
	if o.Retry <= 0 {
		o.Retry = 200 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

type heldLock struct {
	path string
	info os.FileInfo
}

func LockConfigDir(ctx context.Context, configDir string, options LockOptions) (func() error, error) {
	options = options.withDefaults()
	resolved, err := filepath.EvalSymlinks(configDir)
	if err != nil {
		resolved = configDir
	}
	deadline := options.Now().Add(options.Timeout)
	primary, err := acquire(ctx, filepath.Join(configDir, ".oauth_refresh.lock"), deadline, options)
	if err != nil {
		return nil, err
	}
	legacy, err := acquire(ctx, resolved+".lock", deadline, options)
	if err != nil {
		_ = release(primary)
		return nil, err
	}
	held := []*heldLock{&primary, &legacy}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(options.Update)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				now := options.Now()
				for _, lock := range held {
					if !owned(*lock) {
						continue
					}
					if os.Chtimes(lock.path, now, now) == nil {
						if info, err := os.Lstat(lock.path); err == nil {
							lock.info = info
						}
					}
				}
			}
		}
	}()
	var once sync.Once
	var unlockErr error
	return func() error {
		once.Do(func() {
			close(stop)
			<-done
			unlockErr = errors.Join(release(legacy), release(primary))
		})
		return unlockErr
	}, nil
}

func acquire(ctx context.Context, path string, deadline time.Time, options LockOptions) (heldLock, error) {
	for {
		if err := ctx.Err(); err != nil {
			return heldLock{}, err
		}
		err := os.Mkdir(path, 0o700)
		if err == nil {
			info, err := os.Lstat(path)
			if err != nil {
				_ = os.Remove(path)
				return heldLock{}, fmt.Errorf("inspect claude login lock: %w", err)
			}
			return heldLock{path: path, info: info}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return heldLock{}, fmt.Errorf("take claude login lock: %w", err)
		}
		if info, statErr := os.Lstat(path); statErr == nil && info.IsDir() && options.Now().Sub(info.ModTime()) > options.Stale {
			if takeOverStale(path, info) {
				continue
			}
		}
		if !options.Now().Before(deadline) {
			return heldLock{}, ErrLocked
		}
		timer := time.NewTimer(options.Retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return heldLock{}, ctx.Err()
		case <-timer.C:
		}
	}
}

var beforeStaleRename = func(string) {}

func takeOverStale(path string, stale os.FileInfo) bool {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return false
	}
	aside := path + ".stale-" + hex.EncodeToString(suffix)
	beforeStaleRename(path)
	if err := os.Rename(path, aside); err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	moved, err := os.Lstat(aside)
	if err == nil && os.SameFile(moved, stale) && moved.ModTime().Equal(stale.ModTime()) {
		_ = os.Remove(aside)
		return true
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if os.Rename(aside, path) == nil {
			return false
		}
	}
	_ = os.Remove(aside)
	return false
}

func owned(lock heldLock) bool {
	current, err := os.Lstat(lock.path)
	return err == nil && os.SameFile(current, lock.info) && current.ModTime().Equal(lock.info.ModTime())
}

func release(lock heldLock) error {
	if lock.path == "" || !owned(lock) {
		return nil
	}
	if err := os.Remove(lock.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release claude login lock: %w", err)
	}
	return nil
}

func WriteFileAtomic(dir, name string, data []byte, uid int) error {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".tmp-") || name == "." || name == ".." {
		return fmt.Errorf("invalid claude login file name")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open claude login directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmpName := ".tmp-" + hex.EncodeToString(random[:])
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create claude login file: %w", err)
	}
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = root.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if uid >= 0 {
		if err := tmp.Chown(uid, uid); err != nil {
			return fmt.Errorf("own claude login file: %w", err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write claude login file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmpName, name); err != nil {
		return fmt.Errorf("replace claude login file: %w", err)
	}
	keep = true
	handle, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	return handle.Sync()
}
