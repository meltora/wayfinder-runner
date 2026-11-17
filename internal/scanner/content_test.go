package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/redact"
)

func blobTree(files map[string]string) (string, func(context.Context, string) ([]byte, error)) {
	var tree strings.Builder
	byOID := map[string]string{}
	index := 0
	for path, text := range files {
		oid := fmt.Sprintf("%040x", index+1)
		index++
		byOID[oid] = text
		fmt.Fprintf(&tree, "100644 blob %s %d\t%s\x00", oid, len(text), path)
	}
	return tree.String(), func(_ context.Context, oid string) ([]byte, error) { return []byte(byOID[oid]), nil }
}

func TestScanGitTreeWithContentsKeepsOnlySmallAcceptedText(t *testing.T) {
	small := "# Agent instructions\n"
	large := strings.Repeat("a", 100)
	tree, read := blobTree(map[string]string{"AGENTS.md": small, "CLAUDE.md": large, ".env": "X=1\n"})
	got, err := ScanGitTreeWithContents(context.Background(), []byte(tree), read, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Contents, map[string]string{"AGENTS.md": small}) {
		t.Fatalf("contents=%q", got.Contents)
	}
	plain, err := ScanGitTree(context.Background(), []byte(tree), read)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Contents != nil || !reflect.DeepEqual(plain.Items, got.Items) {
		t.Fatalf("plain scan changed: %#v", plain)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "Agent instructions") {
		t.Fatalf("contents leaked into inventory JSON: %s", encoded)
	}
}

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func acceptedItem(path, text string) v1.AgentConfigDiscoveryItem {
	return v1.AgentConfigDiscoveryItem{Path: path, Classification: v1.AgentConfigClassReady, DigestSHA256: digestOf(text)}
}

func defaultContentLimits() ContentLimits {
	return ContentLimits{MaxItemBytes: v1.AgentConfigContentMaxItemBytes, MaxTotalBytes: v1.AgentConfigContentMaxTotalBytes, MaxItems: v1.AgentConfigContentMaxItems, MaxEncodedBytes: 1 << 30}
}

func TestItemContentsMatchDigestsAndSkipUnsafeEntries(t *testing.T) {
	redact.ForgetSecretsForTest()
	t.Cleanup(redact.ForgetSecretsForTest)
	secret := "remembered-secret-value-123"
	redact.RememberSecret(secret)
	items := []v1.AgentConfigDiscoveryItem{
		acceptedItem("AGENTS.md", "hello"),
		acceptedItem("CLAUDE.md", "token "+secret),
		acceptedItem("GEMINI.md", "stale"),
		{Path: "rejected.md", Classification: v1.AgentConfigClassRejected},
		acceptedItem("missing.md", "absent"),
		acceptedItem("skills/a/SKILL.md", "bad\xff"),
	}
	contents := map[string]string{
		"AGENTS.md":         "hello",
		"CLAUDE.md":         "token " + secret,
		"GEMINI.md":         "changed",
		"rejected.md":       "never",
		"skills/a/SKILL.md": "bad\xff",
	}
	got := ItemContents(items, contents, defaultContentLimits())
	want := []v1.AgentConfigItemContent{{Path: "AGENTS.md", Text: "hello"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contents=%q", got)
	}
	if ItemContents(items, nil, defaultContentLimits()) != nil {
		t.Fatal("empty capture must be omitted")
	}
}

func TestItemContentsSkipOversizedEntriesWithoutTruncating(t *testing.T) {
	limits := ContentLimits{MaxItemBytes: 4, MaxTotalBytes: 7, MaxItems: 2, MaxEncodedBytes: 1 << 30}
	items := []v1.AgentConfigDiscoveryItem{acceptedItem("a", "12345"), acceptedItem("b", "1234"), acceptedItem("c", "1234"), acceptedItem("d", "123"), acceptedItem("e", "1")}
	contents := map[string]string{"a": "12345", "b": "1234", "c": "1234", "d": "123", "e": "1"}
	got := ItemContents(items, contents, limits)
	want := []v1.AgentConfigItemContent{{Path: "b", Text: "1234"}, {Path: "d", Text: "123"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contents=%q", got)
	}
}

func TestItemContentsRespectTheEncodedBudget(t *testing.T) {
	escaped := strings.Repeat("<", 10)
	items := []v1.AgentConfigDiscoveryItem{acceptedItem("a", escaped), acceptedItem("b", "plain")}
	contents := map[string]string{"a": escaped, "b": "plain"}
	limits := defaultContentLimits()
	plain, _ := json.Marshal(v1.AgentConfigItemContent{Path: "b", Text: "plain"})
	limits.MaxEncodedBytes = len(plain) + 1
	got := ItemContents(items, contents, limits)
	if !reflect.DeepEqual(got, []v1.AgentConfigItemContent{{Path: "b", Text: "plain"}}) {
		t.Fatalf("contents=%q", got)
	}
}

func TestItemContentsSortByUTF16CodeUnits(t *testing.T) {
	high, astral := "a/Ａ.md", "a/\U0001F600.md"
	items := []v1.AgentConfigDiscoveryItem{acceptedItem(high, "x"), acceptedItem(astral, "y")}
	got := ItemContents(items, map[string]string{high: "x", astral: "y"}, defaultContentLimits())
	if len(got) != 2 || got[0].Path != astral || got[1].Path != high {
		t.Fatalf("order=%q", got)
	}
}
