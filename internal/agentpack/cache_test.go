package agentpack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchiveCacheConvergesConcurrentFetchesAndRecoversCorruption(t *testing.T) {
	body := []byte("signed archive bytes")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	cache := ArchiveCache{Root: filepath.Join(t.TempDir(), "cache"), MaxArchiveBytes: 1 << 20}
	var fetches atomic.Int32
	fetch := func(_ context.Context, writer io.Writer) error {
		fetches.Add(1)
		_, err := writer.Write(body)
		return err
	}

	var wait sync.WaitGroup
	paths := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			path, err := cache.Acquire(context.Background(), digest, fetch)
			paths <- path
			errs <- err
		}()
	}
	wait.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var cachedPath string
	for path := range paths {
		if cachedPath == "" {
			cachedPath = path
		}
		if path != cachedPath {
			t.Fatalf("cache paths differ: %q and %q", cachedPath, path)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("fetches = %d, want one", fetches.Load())
	}
	info, err := os.Lstat(cachedPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
		t.Fatalf("cached archive mode=%v err=%v", info, err)
	}

	if err := os.Chmod(cachedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Acquire(context.Background(), digest, fetch); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 2 {
		t.Fatalf("corrupt cache was not refetched: %d fetches", fetches.Load())
	}
}

func TestArchiveCachePrunesOldestArtifactsWithoutRemovingProtectedDigest(t *testing.T) {
	cache := ArchiveCache{Root: filepath.Join(t.TempDir(), "cache"), MaxArchiveBytes: 1 << 20}
	type artifact struct{ digest, path string }
	artifacts := make([]artifact, 0, 3)
	for _, body := range [][]byte{[]byte("first"), []byte("second"), []byte("third")} {
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])
		path, err := cache.Acquire(context.Background(), digest, func(_ context.Context, writer io.Writer) error {
			_, err := writer.Write(body)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact{digest: digest, path: path})
	}
	for index, artifact := range artifacts {
		when := time.Unix(int64(index+1), 0)
		if err := os.Chtimes(artifact.path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	if err := cache.Prune(1, 1<<20, artifacts[0].digest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifacts[0].path); err != nil {
		t.Fatalf("protected oldest artifact removed: %v", err)
	}
	for _, artifact := range artifacts[1:] {
		if _, err := os.Stat(artifact.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unprotected artifact remains: %v", err)
		}
	}
}

func TestArchiveCacheRejectsOversizeMismatchAndCancellationWithoutPublishing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	cache := ArchiveCache{Root: root, MaxArchiveBytes: 8}
	want := sha256.Sum256([]byte("expected"))
	digest := hex.EncodeToString(want[:])
	tests := map[string]func(context.Context, io.Writer) error{
		"oversize": func(_ context.Context, writer io.Writer) error {
			_, err := writer.Write(bytes.Repeat([]byte("x"), 9))
			return err
		},
		"digest mismatch": func(_ context.Context, writer io.Writer) error {
			_, err := writer.Write([]byte("wrong"))
			return err
		},
		"cancelled": func(ctx context.Context, _ io.Writer) error {
			return ctx.Err()
		},
	}
	for name, fetch := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if name == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if _, err := cache.Acquire(ctx, digest, fetch); err == nil {
				t.Fatal("invalid fetch published")
			}
			if _, err := os.Stat(filepath.Join(root, digest+".tgz")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final cache artifact exists: %v", err)
			}
		})
	}
}

func acquireTestArchive(t *testing.T, cache ArchiveCache, body []byte) (string, string) {
	t.Helper()
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	path, err := cache.Acquire(context.Background(), digest, func(_ context.Context, writer io.Writer) error {
		_, err := writer.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest, path
}

func TestArchiveCacheEvictRemovesRevokedArchivesAndTheirLocks(t *testing.T) {
	cache := ArchiveCache{Root: filepath.Join(t.TempDir(), "cache"), MaxArchiveBytes: 1 << 20}
	revoked, revokedPath := acquireTestArchive(t, cache, []byte("revoked"))
	kept, keptPath := acquireTestArchive(t, cache, []byte("kept"))
	absentSum := sha256.Sum256([]byte("absent"))
	absent := hex.EncodeToString(absentSum[:])

	evicted, skipped, err := cache.Evict([]string{absent, revoked})
	if err != nil {
		t.Fatal(err)
	}
	if len(evicted) != 1 || evicted[0] != revoked || len(skipped) != 0 {
		t.Fatalf("evicted=%v skipped=%v", evicted, skipped)
	}
	for _, path := range []string{revokedPath, filepath.Join(cache.Root, revoked+".lock"), filepath.Join(cache.Root, absent+".lock"), filepath.Join(cache.Root, absent+".tgz")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains after eviction: %v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{keptPath, filepath.Join(cache.Root, kept+".lock")} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("unrevoked %s removed: %v", filepath.Base(path), err)
		}
	}
}

func TestArchiveCacheEvictSkipsADigestWhoseLockIsHeld(t *testing.T) {
	cache := ArchiveCache{Root: filepath.Join(t.TempDir(), "cache"), MaxArchiveBytes: 1 << 20}
	digest, path := acquireTestArchive(t, cache, []byte("in use"))
	locked, release := cache.tryDigestLock(digest)
	if !locked {
		t.Fatal("could not hold the digest lock")
	}
	evicted, skipped, err := cache.Evict([]string{digest})
	release()
	if err != nil {
		t.Fatal(err)
	}
	if len(evicted) != 0 || len(skipped) != 1 || skipped[0] != digest {
		t.Fatalf("evicted=%v skipped=%v", evicted, skipped)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("locked archive removed: %v", err)
	}
	evicted, skipped, err = cache.Evict([]string{digest})
	if err != nil || len(evicted) != 1 || len(skipped) != 0 {
		t.Fatalf("evicted=%v skipped=%v err=%v", evicted, skipped, err)
	}
}

func TestArchiveCacheEvictIgnoresInvalidDigestsAndNeverFollowsSymlinks(t *testing.T) {
	base := t.TempDir()
	cache := ArchiveCache{Root: filepath.Join(base, "cache"), MaxArchiveBytes: 1 << 20}
	digest, _ := acquireTestArchive(t, cache, []byte("linked"))
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedSum := sha256.Sum256([]byte("symlinked"))
	linked := hex.EncodeToString(linkedSum[:])
	if err := os.Symlink(target, filepath.Join(cache.Root, linked+".tgz")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cache.Root, linked+".lock")); err != nil {
		t.Fatal(err)
	}
	evicted, skipped, err := cache.Evict([]string{"../outside/target", strings.ToUpper(digest), "", linked})
	if err != nil {
		t.Fatal(err)
	}
	if len(evicted) != 0 || len(skipped) != 1 || skipped[0] != linked {
		t.Fatalf("evicted=%v skipped=%v", evicted, skipped)
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "keep" {
		t.Fatalf("symlink target changed: %q %v", contents, err)
	}
	if _, err := os.Lstat(filepath.Join(cache.Root, digest+".tgz")); err != nil {
		t.Fatalf("an invalid digest evicted a cached archive: %v", err)
	}
}
