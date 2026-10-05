package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func treeRecord(mode, kind, size, path string) string {
	return fmt.Sprintf("%s %s %s %s\t%s\x00", mode, kind, strings.Repeat("a", 40), size, path)
}

func TestScanGitTreeRawMetadata(t *testing.T) {
	data := []byte("# Agent instructions\n")
	tree := treeRecord("100644", "blob", fmt.Sprint(len(data)), "nested/AGENTS.md") + treeRecord("100644", "blob", "3", "ordinary.txt")
	reads := 0
	got, err := ScanGitTree(context.Background(), []byte(tree), func(context.Context, string) ([]byte, error) { reads++; return data, nil })
	if err != nil || len(got.Items) != 1 || reads != 1 {
		t.Fatalf("got %#v, %v, reads %d", got, err, reads)
	}
	digest := sha256.Sum256(data)
	if got.Items[0].DigestSHA256 != hex.EncodeToString(digest[:]) || got.Items[0].Classification != v1.AgentConfigClassReady {
		t.Fatalf("wrong metadata: %#v", got.Items[0])
	}
}

func TestScanGitTreeRejectsIncompleteWithoutPartialResults(t *testing.T) {
	cases := map[string]string{
		"missing terminator": strings.TrimSuffix(treeRecord("100644", "blob", "0", "AGENTS.md"), "\x00"),
		"gitlink":            treeRecord("160000", "commit", "-", "vendor/module"),
		"bad oid":            "100644 blob invalid 0\tAGENTS.md\x00",
		"bad size":           treeRecord("100644", "blob", "-1", "AGENTS.md"),
		"bad mode":           treeRecord("100600", "blob", "0", "AGENTS.md"),
		"traversal":          treeRecord("100644", "blob", "0", "../AGENTS.md"),
		"duplicate":          treeRecord("100644", "blob", "0", "AGENTS.md") + treeRecord("100644", "blob", "0", "AGENTS.md"),
		"backslash":          treeRecord("100644", "blob", "0", "x\\AGENTS.md"),
	}
	for name, tree := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ScanGitTree(context.Background(), []byte(tree), func(context.Context, string) ([]byte, error) { return []byte{}, nil })
			if !errors.Is(err, ErrIncompleteScan) || len(got.Items) != 0 {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
}

func TestScanGitTreeUnsafeAndSkippedContentNeverRead(t *testing.T) {
	tree := treeRecord("120000", "blob", "20", ".claude") + treeRecord("100644", "blob", "20", ".env") + treeRecord("100644", "blob", "2000000", "AGENTS.md") + treeRecord("100644", "blob", "20", "node_modules/CLAUDE.md")
	got, err := ScanGitTree(context.Background(), []byte(tree), func(context.Context, string) ([]byte, error) { t.Fatal("unsafe blob read"); return nil, nil })
	if err != nil || len(got.Items) != 3 {
		t.Fatalf("got %#v, %v", got, err)
	}
	for _, item := range got.Items {
		if item.Classification != v1.AgentConfigClassRejected || item.DigestSHA256 != "" {
			t.Fatalf("unsafe item %#v", item)
		}
	}
}

func TestScanGitTreeContentClassifications(t *testing.T) {
	for _, tc := range []struct {
		name, content, mode string
		source              v1.AgentConfigSourceType
	}{
		{"binary", "\x00", "100644", v1.AgentConfigBinary},
		{"secret", "token=abcdefghijklmnopqrstuv", "100644", v1.AgentConfigCredential},
		{"executable", "instructions", "100755", v1.AgentConfigExecutableExtension},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ScanGitTree(context.Background(), []byte(treeRecord(tc.mode, "blob", fmt.Sprint(len(tc.content)), "AGENTS.md")), func(context.Context, string) ([]byte, error) { return []byte(tc.content), nil })
			if err != nil || len(got.Items) != 1 || got.Items[0].SourceType != tc.source {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
}

func TestScanGitTreeLimitsAndReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, tree string
		read       func(context.Context, string) ([]byte, error)
		want       error
	}{
		{"path", treeRecord("100644", "blob", "0", strings.Repeat("a", MaxPathSize)+"/AGENTS.md"), nil, ErrScanLimit},
		{"tree bytes", strings.Repeat("x", MaxGitTreeBytes+1), nil, ErrScanLimit},
		{"read failure", treeRecord("100644", "blob", "0", "AGENTS.md"), func(context.Context, string) ([]byte, error) { return nil, errors.New("read failed") }, ErrIncompleteScan},
		{"size mismatch", treeRecord("100644", "blob", "1", "AGENTS.md"), func(context.Context, string) ([]byte, error) { return nil, nil }, ErrIncompleteScan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ScanGitTree(context.Background(), []byte(tc.tree), tc.read)
			if !errors.Is(err, tc.want) || len(got.Items) != 0 {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanGitTree(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestScanGitTreeTraversalAndAggregateLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count, size int
	}{
		{"items", MaxItems + 1, 0}, {"aggregate bytes", MaxGitBlobBytes/MaxFileSize + 1, MaxFileSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tree strings.Builder
			for i := 0; i < tc.count; i++ {
				tree.WriteString(treeRecord("100644", "blob", fmt.Sprint(tc.size), fmt.Sprintf("dir%d/AGENTS.md", i)))
			}
			blob := make([]byte, tc.size)
			inventory, err := ScanGitTree(context.Background(), []byte(tree.String()), func(context.Context, string) ([]byte, error) { return blob, nil })
			if !errors.Is(err, ErrScanLimit) || len(inventory.Items) != 0 {
				t.Fatalf("partial inventory: %#v %v", inventory, err)
			}
		})
	}
	var tree strings.Builder
	for i := 0; i <= MaxGitTreeEntries; i++ {
		tree.WriteString(treeRecord("100644", "blob", "0", fmt.Sprintf("ordinary%d", i)))
	}
	if got, err := ScanGitTree(context.Background(), []byte(tree.String()), nil); !errors.Is(err, ErrScanLimit) || len(got.Items) != 0 {
		t.Fatalf("unbounded traversal: %#v %v", got, err)
	}
}

func TestScanGitTreeCancellationDuringReadAndRawNames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if got, err := ScanGitTree(ctx, []byte(treeRecord("100644", "blob", "0", "AGENTS.md")), func(context.Context, string) ([]byte, error) { cancel(); return nil, nil }); !errors.Is(err, context.Canceled) || len(got.Items) != 0 {
		t.Fatalf("late result: %#v %v", got, err)
	}
	got, err := ScanGitTree(context.Background(), []byte(treeRecord("100644", "blob", "0", "folder with\ttab/AGENTS.md")), func(context.Context, string) ([]byte, error) { return nil, nil })
	if err != nil || len(got.Items) != 1 || got.Items[0].Path != "folder with\ttab/AGENTS.md" {
		t.Fatalf("raw path corrupted: %#v %v", got, err)
	}
}
