package capability

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
)

type ProviderVersionCache struct {
	mu      sync.Mutex
	entries map[string]providerVersionEntry
}

type providerVersionEntry struct {
	fingerprint string
	output      string
}

func NewProviderVersionCache() *ProviderVersionCache {
	return &ProviderVersionCache{entries: map[string]providerVersionEntry{}}
}

func (d *Detector) WithProviderVersionCache(cache *ProviderVersionCache) *Detector {
	configured := *d
	configured.providerVersionCache = cache
	return &configured
}

func (d *Detector) cachedProviderVersion(ctx context.Context, name, binary, resolved string) (string, error) {
	cache := d.providerVersionCache
	if cache == nil {
		return d.runProviderVersion(ctx, name, binary)
	}
	fingerprint, ok := providerBinaryFingerprint(binary, resolved, d.providerEnvironment[name])
	if !ok {
		return d.runProviderVersion(ctx, name, binary)
	}
	cache.mu.Lock()
	entry, found := cache.entries[name]
	cache.mu.Unlock()
	if found && entry.fingerprint == fingerprint {
		return entry.output, nil
	}
	output, err := d.runProviderVersion(ctx, name, binary)
	if err != nil {
		return output, err
	}
	cache.mu.Lock()
	cache.entries[name] = providerVersionEntry{fingerprint: fingerprint, output: output}
	cache.mu.Unlock()
	return output, nil
}

func providerBinaryFingerprint(binary, resolved string, env []string) (string, bool) {
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	parts := []string{binary, resolved, strconv.FormatInt(info.Size(), 10), strconv.FormatInt(info.ModTime().UnixNano(), 10)}
	parts = append(parts, env...)
	return strings.Join(parts, "\x00"), true
}
