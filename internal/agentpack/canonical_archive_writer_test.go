package agentpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func sampleArchiveContent() ArchiveContent {
	return ArchiveContent{
		PackKey:  "fixture",
		Provider: "claude-code",
		Version:  "1.0.0",
		Files: map[string][]byte{
			"WORKFLOW.md":                []byte("# workflow\n"),
			".claude-plugin/plugin.json": []byte(`{"name":"fixture"}` + "\n"),
			"skills/a/SKILL.md":          []byte("---\nname: a\n---\n"),
			strings.Repeat("d", 60) + "/" + strings.Repeat("e", 60) + "/long.md": []byte("long path\n"),
		},
	}
}

func writeSampleArchive(t *testing.T, content ArchiveContent) ([]byte, ArchiveSummary) {
	t.Helper()
	var out bytes.Buffer
	summary, err := WriteCanonicalArchive(&out, content)
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), summary
}

func TestWriteCanonicalArchiveIsDeterministicAndAddressedByDigest(t *testing.T) {
	first, summary := writeSampleArchive(t, sampleArchiveContent())
	for attempt := 0; attempt < 5; attempt++ {
		again, againSummary := writeSampleArchive(t, sampleArchiveContent())
		if !bytes.Equal(first, again) || againSummary.Digest != summary.Digest {
			t.Fatal("archive bytes differ between identical builds")
		}
	}
	sum := sha256.Sum256(first)
	if summary.Digest != hex.EncodeToString(sum[:]) || summary.Bytes != int64(len(first)) || summary.Files != 4 {
		t.Fatalf("summary = %+v", summary)
	}
	header := first[:10]
	if !bytes.Equal(header[:8], []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0}) || header[8] != 2 {
		t.Fatalf("gzip header = %x", header)
	}
}

func TestWriteCanonicalArchiveDigestChangesWithEveryIdentityInput(t *testing.T) {
	_, base := writeSampleArchive(t, sampleArchiveContent())
	mutations := map[string]func(*ArchiveContent){
		"file byte": func(c *ArchiveContent) { c.Files["WORKFLOW.md"] = []byte("# workflow!\n") },
		"file name": func(c *ArchiveContent) {
			c.Files["WORKFLOW2.md"] = c.Files["WORKFLOW.md"]
			delete(c.Files, "WORKFLOW.md")
		},
		"version":  func(c *ArchiveContent) { c.Version = "1.0.1" },
		"provider": func(c *ArchiveContent) { c.Provider = "codex" },
		"pack key": func(c *ArchiveContent) { c.PackKey = "fixture-two" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			content := sampleArchiveContent()
			mutate(&content)
			_, summary := writeSampleArchive(t, content)
			if summary.Digest == base.Digest {
				t.Fatal("digest did not change")
			}
		})
	}
}

func TestWriteCanonicalArchiveRoundTripsThroughReader(t *testing.T) {
	archive, summary := writeSampleArchive(t, sampleArchiveContent())
	parsed, err := ParseCanonicalArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Digest != summary.Digest || parsed.Manifest.PackKey != "fixture" || len(parsed.Files) != 4 {
		t.Fatalf("parsed = %+v", parsed)
	}
	long := strings.Repeat("d", 60) + "/" + strings.Repeat("e", 60) + "/long.md"
	if string(parsed.Files[long]) != "long path\n" || parsed.Names[0] != ".claude-plugin/plugin.json" {
		t.Fatalf("files = %v names = %v", parsed.Files, parsed.Names)
	}
}

func TestEncodeUSTARHeaderUsesNamePrefixSplitOnlyWhenNeeded(t *testing.T) {
	short, err := encodeUSTARHeader("WORKFLOW.md", 11)
	if err != nil {
		t.Fatal(err)
	}
	if string(short[0:11]) != "WORKFLOW.md" || short[345] != 0 || string(short[100:108]) != "0000444\x00" ||
		string(short[124:136]) != "00000000013\x00" || short[156] != '0' || string(short[257:265]) != "ustar\x0000" {
		t.Fatalf("short header = %q", short[:])
	}
	long := strings.Repeat("d", 60) + "/" + strings.Repeat("e", 60) + "/long.md"
	header, err := encodeUSTARHeader(long, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimRight(header[345:500], "\x00")) != strings.Repeat("d", 60) ||
		string(bytes.TrimRight(header[0:100], "\x00")) != strings.Repeat("e", 60)+"/long.md" {
		t.Fatalf("long header split wrong")
	}
	if _, err := encodeUSTARHeader(strings.Repeat("x", 101), 1); err == nil {
		t.Fatal("unsplittable path encoded")
	}
}

func TestWriteCanonicalArchiveRejectsInvalidContent(t *testing.T) {
	tooMany := map[string][]byte{}
	for index := 0; index <= ArchiveMaxFiles; index++ {
		tooMany[fmt.Sprintf("f%04d", index)] = []byte("x")
	}
	tooLarge := map[string][]byte{}
	for index := 0; index < 65; index++ {
		tooLarge[fmt.Sprintf("f%02d", index)] = make([]byte, ArchiveMaxFileBytes)
	}
	cases := map[string]ArchiveContent{
		"no files":      {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: map[string][]byte{}},
		"bad provider":  {PackKey: "fixture", Provider: "claude", Version: "1.0.0", Files: map[string][]byte{"a": nil}},
		"bad version":   {PackKey: "fixture", Provider: "claude-code", Version: "v1", Files: map[string][]byte{"a": nil}},
		"bad pack key":  {PackKey: "Fixture", Provider: "claude-code", Version: "1.0.0", Files: map[string][]byte{"a": nil}},
		"reserved name": {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: map[string][]byte{"signature": nil}},
		"bad path":      {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: map[string][]byte{"a b": nil}},
		"file too big":  {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: map[string][]byte{"a": make([]byte, ArchiveMaxFileBytes+1)}},
		"too many":      {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: tooMany},
		"total":         {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: tooLarge},
		"case collide":  {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: map[string][]byte{"A.md": nil, "a.md": nil}},
		"dir collide":   {PackKey: "fixture", Provider: "claude-code", Version: "1.0.0", Files: map[string][]byte{"docs": nil, "docs/a.md": nil}},
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if _, err := WriteCanonicalArchive(&out, content); err == nil {
				t.Fatal("invalid content written")
			}
			if out.Len() != 0 {
				t.Fatal("rejected content wrote output")
			}
		})
	}
	if _, err := WriteCanonicalArchive(nil, sampleArchiveContent()); err == nil {
		t.Fatal("nil writer accepted")
	}
}
