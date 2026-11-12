package capability

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cachedVersionDetector(t *testing.T, cache *ProviderVersionCache, binary string, calls *int) *Detector {
	t.Helper()
	detector := fakeDetector().WithProviderBinaries(map[string]string{"opencode": binary}).WithProviderVersionCache(cache)
	detector.LookPath = func(name string) (string, error) { return name, nil }
	detector.RunVersion = func(_ context.Context, name string, _ ...string) (string, error) {
		if name == binary {
			*calls++
			return "1.2.3", nil
		}
		return name + " version 1.0", nil
	}
	return detector
}

func writeFakeBinary(t *testing.T, path, body string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestProviderVersionCacheRunsVersionOnceForUnchangedBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "opencode")
	writeFakeBinary(t, binary, "v1", time.Unix(1_700_000_000, 0))
	cache := NewProviderVersionCache()
	calls := 0
	for range 3 {
		manifest := cachedVersionDetector(t, cache, binary, &calls).Collect(context.Background())
		if got := providerCapability(t, manifest, "opencode").Version; got != "1.2.3" {
			t.Fatalf("opencode version = %q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("version probe ran %d times, want 1", calls)
	}
}

func TestProviderVersionCacheRerunsWhenBinaryChanges(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "opencode")
	writeFakeBinary(t, binary, "v1", time.Unix(1_700_000_000, 0))
	cache := NewProviderVersionCache()
	calls := 0
	cachedVersionDetector(t, cache, binary, &calls).Collect(context.Background())
	writeFakeBinary(t, binary, "v2-longer", time.Unix(1_700_000_100, 0))
	cachedVersionDetector(t, cache, binary, &calls).Collect(context.Background())
	if calls != 2 {
		t.Fatalf("version probe ran %d times, want 2", calls)
	}
}

func TestProviderVersionWithoutCacheRunsEveryCollect(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "opencode")
	writeFakeBinary(t, binary, "v1", time.Unix(1_700_000_000, 0))
	calls := 0
	for range 2 {
		cachedVersionDetector(t, nil, binary, &calls).Collect(context.Background())
	}
	if calls != 2 {
		t.Fatalf("version probe ran %d times, want 2", calls)
	}
}
