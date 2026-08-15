package agentpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	DefaultMaxArchiveBytes int64 = 64 << 20
	DefaultMaxCacheBytes   int64 = 512 << 20
	DefaultMaxCacheEntries       = 32
)

var (
	ErrArchiveDigestMismatch = errors.New("Agent Pack archive digest mismatch")
	ErrArchiveInvalid        = errors.New("Agent Pack archive is invalid")
	ErrArchiveSizeLimit      = errors.New("Agent Pack archive exceeds the size limit")
)

type ArchiveFetcher func(context.Context, io.Writer) error

// ArchiveCache stores exact signed archive bytes by SHA-256. Publication uses
// fsync plus atomic rename while an OS-backed per-digest lock ensures that
// concurrent jobs and runner processes converge on one verified artifact.
type ArchiveCache struct {
	Root            string
	MaxArchiveBytes int64
}

// Prune removes least-recently-used immutable archives until both bounds are
// met. The digest in active use is protected and locked entries are skipped.
func (cache ArchiveCache) Prune(maxEntries int, maxBytes int64, protectedDigest string) error {
	if maxEntries < 1 || maxBytes < 1 {
		return fmt.Errorf("Agent Pack cache retention bounds must be positive")
	}
	if protectedDigest != "" && !validSHA256(protectedDigest) {
		return fmt.Errorf("protected Agent Pack digest is invalid")
	}
	if err := ensurePrivateCacheRoot(cache.Root); err != nil {
		return err
	}
	entries, err := os.ReadDir(cache.Root)
	if err != nil {
		return fmt.Errorf("scan Agent Pack cache for pruning: %w", err)
	}
	type candidate struct {
		name, digest string
		size         int64
		modified     time.Time
	}
	archives := make([]candidate, 0, len(entries))
	var totalBytes int64
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".tgz") {
			continue
		}
		digest := strings.TrimSuffix(name, ".tgz")
		if !validSHA256(digest) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
			continue
		}
		archives = append(archives, candidate{name: name, digest: digest, size: info.Size(), modified: info.ModTime()})
		totalBytes += info.Size()
	}
	sort.Slice(archives, func(i, j int) bool {
		if archives[i].modified.Equal(archives[j].modified) {
			return archives[i].name < archives[j].name
		}
		return archives[i].modified.Before(archives[j].modified)
	})
	remaining := len(archives)
	for _, archive := range archives {
		if remaining <= maxEntries && totalBytes <= maxBytes {
			break
		}
		if archive.digest == protectedDigest {
			continue
		}
		locked, release := cache.tryDigestLock(archive.digest)
		if !locked {
			continue
		}
		removeErr := os.Remove(filepath.Join(cache.Root, archive.name))
		release()
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("prune Agent Pack cache artifact: %w", removeErr)
		}
		remaining--
		totalBytes -= archive.size
	}
	return nil
}

func (cache ArchiveCache) tryDigestLock(digest string) (bool, func()) {
	lockPath := filepath.Join(cache.Root, digest+".lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, func() {}
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return false, func() {}
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return false, func() {}
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return false, func() {}
	}
	return true, func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
	}
}

func (cache ArchiveCache) Acquire(ctx context.Context, digest string, fetch ArchiveFetcher) (string, error) {
	if !validSHA256(digest) {
		return "", fmt.Errorf("cache digest must be lowercase SHA-256")
	}
	if fetch == nil {
		return "", fmt.Errorf("archive fetcher is required")
	}
	limit := cache.MaxArchiveBytes
	if limit <= 0 || limit > DefaultMaxArchiveBytes {
		limit = DefaultMaxArchiveBytes
	}
	if err := ensurePrivateCacheRoot(cache.Root); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(cache.Root)
	if err != nil {
		return "", fmt.Errorf("open Agent Pack cache: %w", err)
	}
	defer func() { _ = root.Close() }()

	lockPath := filepath.Join(cache.Root, digest+".lock")
	lockFD, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", fmt.Errorf("open Agent Pack cache lock: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), lockPath)
	if lock == nil {
		_ = unix.Close(lockFD)
		return "", fmt.Errorf("open Agent Pack cache lock")
	}
	defer func() { _ = lock.Close() }()
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("Agent Pack cache lock must be a private regular file")
	}
	if err := acquireFileLock(ctx, lock); err != nil {
		return "", err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	finalName := digest + ".tgz"
	if ok, err := verifyCachedArchive(root, finalName, digest, limit); err != nil {
		return "", err
	} else if ok {
		return filepath.Join(cache.Root, finalName), nil
	}
	if err := removeStaleDigestTemps(cache.Root, digest); err != nil {
		return "", err
	}

	temporary, err := os.CreateTemp(cache.Root, "."+digest+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create Agent Pack cache temporary: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure Agent Pack cache temporary: %w", err)
	}
	hash := sha256.New()
	bounded := &boundedArchiveWriter{writer: io.MultiWriter(temporary, hash), remaining: limit}
	if err := fetch(ctx, bounded); err != nil {
		return "", fmt.Errorf("fetch Agent Pack archive: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if bounded.written == 0 {
		return "", ErrArchiveInvalid
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != digest {
		return "", ErrArchiveDigestMismatch
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("sync Agent Pack cache temporary: %w", err)
	}
	if err := temporary.Chmod(0o400); err != nil {
		return "", fmt.Errorf("make Agent Pack cache artifact immutable: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close Agent Pack cache temporary: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(cache.Root, finalName)); err != nil {
		return "", fmt.Errorf("publish Agent Pack cache artifact: %w", err)
	}
	committed = true
	if dir, err := os.Open(cache.Root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return filepath.Join(cache.Root, finalName), nil
}

type boundedArchiveWriter struct {
	writer    io.Writer
	remaining int64
	written   int64
}

func (writer *boundedArchiveWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > writer.remaining {
		return 0, ErrArchiveSizeLimit
	}
	written, err := writer.writer.Write(data)
	writer.remaining -= int64(written)
	writer.written += int64(written)
	return written, err
}

func ensurePrivateCacheRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("Agent Pack cache root must be a clean absolute path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create Agent Pack cache: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect Agent Pack cache: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("Agent Pack cache must be a private real directory")
	}
	return nil
}

func acquireFileLock(ctx context.Context, file *os.File) error {
	for {
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock Agent Pack cache: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func verifyCachedArchive(root *os.Root, name, digest string, limit int64) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect cached Agent Pack: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 || info.Size() <= 0 || info.Size() > limit {
		if err := root.Remove(name); err != nil {
			return false, fmt.Errorf("remove invalid cached Agent Pack: %w", err)
		}
		return false, nil
	}
	file, err := root.Open(name)
	if err != nil {
		return false, fmt.Errorf("open cached Agent Pack: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return false, fmt.Errorf("hash cached Agent Pack")
	}
	if hex.EncodeToString(hash.Sum(nil)) == digest {
		now := time.Now()
		if err := root.Chtimes(name, now, now); err != nil {
			return false, fmt.Errorf("refresh cached Agent Pack access time: %w", err)
		}
		return true, nil
	}
	if err := root.Remove(name); err != nil {
		return false, fmt.Errorf("remove corrupt cached Agent Pack: %w", err)
	}
	return false, nil
}

func removeStaleDigestTemps(root, digest string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("scan Agent Pack cache: %w", err)
	}
	prefix := "." + digest + "-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".tmp") {
			if err := os.Remove(filepath.Join(root, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove stale Agent Pack cache temporary: %w", err)
			}
		}
	}
	return nil
}
