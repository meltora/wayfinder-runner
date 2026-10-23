package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	maxCandidateCaches   = 4
	candidateRacyWindow  = 2 * time.Second
	candidateCachesDir   = "candidates"
	candidateIndexSuffix = ".index"
)

type copiedFile struct {
	source statKey
	target statKey
}

type candidateCache struct {
	files  string
	index  string
	copied map[string]copiedFile
	used   uint64
}

func (t *trustedResultRepository) candidateCache(relPath string) (*candidateCache, error) {
	if cache, ok := t.caches[relPath]; ok {
		t.cacheClock++
		cache.used = t.cacheClock
		return cache, nil
	}
	limit := t.cacheLimit
	if limit < 1 {
		limit = 1
	}
	for len(t.caches) >= limit {
		if err := t.evictCandidateCache(); err != nil {
			return nil, err
		}
	}
	parent := filepath.Join(t.root, candidateCachesDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	t.cacheSerial++
	files := filepath.Join(parent, strconv.Itoa(t.cacheSerial))
	if err := os.RemoveAll(files); err != nil {
		return nil, err
	}
	if err := os.Remove(files + candidateIndexSuffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.Mkdir(files, 0o700); err != nil {
		return nil, err
	}
	t.cacheClock++
	cache := &candidateCache{files: files, index: files + candidateIndexSuffix, used: t.cacheClock}
	if t.caches == nil {
		t.caches = map[string]*candidateCache{}
	}
	t.caches[relPath] = cache
	return cache, nil
}

func (t *trustedResultRepository) evictCandidateCache() error {
	oldest := ""
	for relPath, cache := range t.caches {
		if oldest == "" || cache.used < t.caches[oldest].used {
			oldest = relPath
		}
	}
	return t.dropCandidateCache(oldest)
}

func (t *trustedResultRepository) dropCandidateCache(relPath string) error {
	cache, ok := t.caches[relPath]
	if !ok {
		return nil
	}
	delete(t.caches, relPath)
	removeErr := os.RemoveAll(cache.files)
	indexErr := os.Remove(cache.index)
	if errors.Is(indexErr, fs.ErrNotExist) {
		indexErr = nil
	}
	return errors.Join(removeErr, indexErr)
}

func (t *trustedResultRepository) indexedGit(cache *candidateCache, extra ...string) trustedGitOptions {
	return trustedGitOptions{
		gitDir: t.gitDir, workTree: cache.files,
		env: append([]string{"GIT_INDEX_FILE=" + cache.index}, extra...),
	}
}
