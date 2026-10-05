package agentpack

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	goldenConformanceSource = "testdata/conformance/source"
	goldenConformanceDigest = "a0941e4ea443cb83a16ac465891ae2f9292196fc7d17fa32964828dc8ea46970"
)

var goldenBuildOptions = ArchiveBuildOptions{PackKey: "fixture", Provider: "claude-code", Version: "1.0.0"}

func buildGoldenArchive(t *testing.T, source string) ([]byte, ArchiveSummary) {
	t.Helper()
	var out bytes.Buffer
	summary, err := BuildArchiveFromDirectory(source, goldenBuildOptions, &out)
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), summary
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(from, path)
		target := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildArchiveFromDirectoryMatchesCommittedGoldenDigest(t *testing.T) {
	first, summary := buildGoldenArchive(t, goldenConformanceSource)
	second, again := buildGoldenArchive(t, goldenConformanceSource)
	if !bytes.Equal(first, second) || summary.Digest != again.Digest {
		t.Fatal("golden build is not deterministic within one process")
	}
	if summary.Digest != goldenConformanceDigest {
		t.Fatalf("golden digest = %s, want %s", summary.Digest, goldenConformanceDigest)
	}
	if summary.Files != 5 {
		t.Fatalf("files = %d", summary.Files)
	}
}

func TestBuildArchiveFromDirectoryIgnoresModesTimesAndWalkOrder(t *testing.T) {
	source := t.TempDir()
	copyTree(t, goldenConformanceSource, source)
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
		}
		return os.Chtimes(path, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, summary := buildGoldenArchive(t, source)
	if summary.Digest != goldenConformanceDigest {
		t.Fatalf("metadata changed the digest: %s", summary.Digest)
	}
	if err := os.WriteFile(filepath.Join(source, "WORKFLOW.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, changed := buildGoldenArchive(t, source); changed.Digest == goldenConformanceDigest {
		t.Fatal("content change did not change the digest")
	}
}

func TestBuildArchiveFromDirectoryRejectsUnsafeSourcesNamingThePath(t *testing.T) {
	cases := map[string]struct {
		prepare func(t *testing.T, source string)
		path    string
	}{
		"symlink": {func(t *testing.T, source string) {
			if err := os.Symlink("WORKFLOW.md", filepath.Join(source, "link.md")); err != nil {
				t.Fatal(err)
			}
		}, "link.md"},
		"directory symlink": {func(t *testing.T, source string) {
			if err := os.Symlink("skills", filepath.Join(source, "linked")); err != nil {
				t.Fatal(err)
			}
		}, "linked"},
		"fifo": {func(t *testing.T, source string) {
			if err := syscall.Mkfifo(filepath.Join(source, "pipe"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "pipe"},
		"socket": {func(t *testing.T, source string) {
			listener, err := net.Listen("unix", filepath.Join(source, "s"))
			if err != nil {
				t.Skipf("unix socket unavailable: %v", err)
			}
			t.Cleanup(func() { _ = listener.Close() })
		}, "s"},
		"reserved manifest": {func(t *testing.T, source string) {
			writeSourceFile(t, source, "agent-pack.json", "{}")
		}, "agent-pack.json"},
		"reserved legacy manifest": {func(t *testing.T, source string) {
			writeSourceFile(t, source, "pack.yaml", "x")
		}, "pack.yaml"},
		"reserved signature": {func(t *testing.T, source string) {
			writeSourceFile(t, source, "signature", "x")
		}, "signature"},
		"charset": {func(t *testing.T, source string) {
			writeSourceFile(t, source, "has space.md", "x")
		}, "has space.md"},
		"non ascii": {func(t *testing.T, source string) {
			writeSourceFile(t, source, "café.md", "x")
		}, "caf"},
		"oversize": {func(t *testing.T, source string) {
			writeSourceFile(t, source, "big.bin", strings.Repeat("x", ArchiveMaxFileBytes+1))
		}, "big.bin"},
		"unsplittable": {func(t *testing.T, source string) {
			writeSourceFile(t, source, strings.Repeat("n", 120), "x")
		}, strings.Repeat("n", 120)},
		"os metadata ds store":        {nestedSource(".DS_Store"), ".DS_Store"},
		"nested os metadata ds store": {nestedSource("skills/.DS_Store"), "skills/.DS_Store"},
		"os metadata thumbs":          {nestedSource("Thumbs.db"), "Thumbs.db"},
		"folded os metadata thumbs":   {nestedSource("skills/thumbs.DB"), "skills/thumbs.DB"},
		"apple double":                {nestedSource("._WORKFLOW.md"), "._WORKFLOW.md"},
		"nested apple double":         {nestedSource("skills/._SKILL.md"), "skills/._SKILL.md"},
		"git directory": {func(t *testing.T, source string) {
			if err := os.Mkdir(filepath.Join(source, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, ".git"},
		"git config":               {nestedSource(".git/config"), ".git"},
		"nested git file":          {nestedSource("skills/.git"), "skills/.git"},
		"folded git directory":     {nestedSource("skills/.GIT/HEAD"), "skills/.GIT"},
		"nested manifest":          {nestedSource("skills/agent-pack.json"), "skills/agent-pack.json"},
		"manifest directory":       {nestedSource("agent-pack.json/x.md"), "agent-pack.json"},
		"proto file":               {nestedSource("__proto__"), "__proto__"},
		"proto directory":          {nestedSource("skills/__proto__/x.md"), "skills/__proto__"},
		"folded nested manifest":   {nestedSource("sub/AGENT-PACK.JSON"), "sub/AGENT-PACK.JSON"},
		"folded root manifest":     {nestedSource("Agent-Pack.json"), "Agent-Pack.json"},
		"folded proto file":        {nestedSource("__PROTO__"), "__PROTO__"},
		"folded proto directory":   {nestedSource("skills/__Proto__/x.md"), "skills/__Proto__"},
		"folded root git":          {nestedSource(".GIT/config"), ".GIT"},
		"folded os metadata lower": {nestedSource(".ds_store"), ".ds_store"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			copyTree(t, goldenConformanceSource, source)
			test.prepare(t, source)
			var out bytes.Buffer
			_, err := BuildArchiveFromDirectory(source, goldenBuildOptions, &out)
			if err == nil {
				t.Fatal("unsafe source built")
			}
			if !strings.Contains(err.Error(), test.path) {
				t.Fatalf("error %q does not name %q", err, test.path)
			}
			if reason, ok := sourceRefusalReasons[name]; ok && !strings.Contains(err.Error(), strconv.Quote(test.path)+" "+reason) {
				t.Fatalf("error %q does not refuse %q as %s", err, test.path, reason)
			}
			if out.Len() != 0 {
				t.Fatal("failed build wrote output")
			}
		})
	}
}

func TestBuildArchiveFromDirectoryRejectsInvalidOptionsAndEmptySources(t *testing.T) {
	var out bytes.Buffer
	if _, err := BuildArchiveFromDirectory(goldenConformanceSource, ArchiveBuildOptions{PackKey: "fixture", Provider: "claude-code", Version: "1"}, &out); err == nil {
		t.Fatal("invalid version accepted")
	}
	if _, err := BuildArchiveFromDirectory(t.TempDir(), goldenBuildOptions, &out); err == nil {
		t.Fatal("empty source accepted")
	}
	if _, err := BuildArchiveFromDirectory(filepath.Join(t.TempDir(), "missing"), goldenBuildOptions, &out); err == nil {
		t.Fatal("missing source accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	writeSourceFile(t, filepath.Dir(file), "file", "x")
	if _, err := BuildArchiveFromDirectory(file, goldenBuildOptions, &out); err == nil {
		t.Fatal("file source accepted")
	}
	if !archiveSourcePathAllowed("a/b.c") || archiveSourcePathAllowed("a/\xff") {
		t.Fatal("source path charset rule is wrong")
	}
}

var sourceRefusalReasons = map[string]string{
	"os metadata ds store":        "is operating-system metadata",
	"nested os metadata ds store": "is operating-system metadata",
	"os metadata thumbs":          "is operating-system metadata",
	"folded os metadata thumbs":   "is operating-system metadata",
	"apple double":                "is operating-system metadata",
	"nested apple double":         "is operating-system metadata",
	"git directory":               "is version-control metadata",
	"git config":                  "is version-control metadata",
	"nested git file":             "is version-control metadata",
	"folded git directory":        "is version-control metadata",
	"nested manifest":             "uses the reserved manifest name",
	"manifest directory":          "uses the reserved manifest name",
	"proto file":                  "uses the reserved __proto__ name",
	"proto directory":             "uses the reserved __proto__ name",
	"folded nested manifest":      "uses the reserved manifest name",
	"folded root manifest":        "uses the reserved manifest name",
	"folded proto file":           "uses the reserved __proto__ name",
	"folded proto directory":      "uses the reserved __proto__ name",
	"folded root git":             "is version-control metadata",
	"folded os metadata lower":    "is operating-system metadata",
}

func nestedSource(relative string) func(t *testing.T, source string) {
	return func(t *testing.T, source string) {
		t.Helper()
		path := filepath.Join(source, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildArchiveFromDirectoryKeepsNamesThatOnlyResembleReservedOnes(t *testing.T) {
	source := t.TempDir()
	copyTree(t, goldenConformanceSource, source)
	for _, relative := range []string{".gitignore", ".github/workflow.yml", "docs/agent-pack.json.md", "constructor", "prototype/toString", "a.DS_Store", "x._y", "__PROTO__x.md", "docs/.GITIGNORE", "AGENT-PACK.JSON.md"} {
		nestedSource(relative)(t, source)
	}
	var out bytes.Buffer
	summary, err := BuildArchiveFromDirectory(source, goldenBuildOptions, &out)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := summary.Manifest.Files[".github/workflow.yml"]; !ok || summary.Files != 15 {
		t.Fatalf("summary = %+v", summary)
	}
}

func writeSourceFile(t *testing.T, source, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
