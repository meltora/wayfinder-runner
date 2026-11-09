package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotKeepsAnUnchangedSymlinkAndRetargetsAChangedOne(t *testing.T) {
	f := newAgentResultTest(t)
	f.repository().Trusted.racyWindow = 0
	writeAgentFile(t, f.checkout, "a.txt", "a\n")
	writeAgentFile(t, f.checkout, "b.txt", "b\n")
	link := filepath.Join(f.checkout, "current")
	if err := os.Symlink("a.txt", link); err != nil {
		t.Fatal(err)
	}
	first := f.publish(t)
	cached := filepath.Join(singleCandidateCache(t, f).files, "current")
	before, err := os.Lstat(cached)
	if err != nil {
		t.Fatal(err)
	}

	if second := f.publish(t); second != first {
		t.Fatalf("an unchanged symlink changed the snapshot: %s, want %s", second, first)
	}
	after, err := os.Lstat(cached)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("an unchanged symlink was recreated")
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.txt", link); err != nil {
		t.Fatal(err)
	}
	third := f.publish(t)
	if got := f.publishedContent(t, third, "current"); got != "b.txt" {
		t.Fatalf("published symlink target %q, want b.txt", got)
	}
	if got, err := os.Readlink(cached); err != nil || got != "b.txt" {
		t.Fatalf("cached symlink = %q, %v", got, err)
	}
}

func TestSnapshotCopiesAgainAFileWhoseCachedCopyIsMissing(t *testing.T) {
	f := newAgentResultTest(t)
	f.repository().Trusted.racyWindow = 0
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(f.checkout, "feature.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	first := f.publish(t)
	cached := filepath.Join(singleCandidateCache(t, f).files, "feature.txt")
	if err := os.Remove(cached); err != nil {
		t.Fatal(err)
	}

	if second := f.publish(t); second != first {
		t.Fatalf("snapshot after losing the cached copy = %s, want %s", second, first)
	}
	if content, err := os.ReadFile(cached); err != nil || string(content) != "real\n" {
		t.Fatalf("cached copy = %q, %v", content, err)
	}
}

func TestSnapshotRefusesAndRebuildsACandidateCacheChangedOutsideTheRunner(t *testing.T) {
	for name, tamper := range map[string]func(t *testing.T, path string){
		"rewritten": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("tampered\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(path, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAgentResultTest(t)
			f.repository().Trusted.racyWindow = 0
			writeAgentFile(t, f.checkout, "feature.txt", "real\n")
			past := time.Now().Add(-time.Hour)
			if err := os.Chtimes(filepath.Join(f.checkout, "feature.txt"), past, past); err != nil {
				t.Fatal(err)
			}
			first := f.publish(t)
			tamper(t, filepath.Join(singleCandidateCache(t, f).files, "feature.txt"))

			if sha, err := f.jr.commitAgentChanges(context.Background(), f.repository()); err == nil {
				t.Fatalf("a snapshot over a tampered cache was published as %s", sha)
			}
			if caches := f.repository().Trusted.caches; len(caches) != 0 {
				t.Fatalf("a refused snapshot kept %d candidate caches", len(caches))
			}
			if third := f.publish(t); third != first {
				t.Fatalf("the rebuilt snapshot = %s, want %s", third, first)
			}
		})
	}
}
