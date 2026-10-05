package agentpack

import (
	"fmt"
	"strings"
	"testing"
)

func sampleArchiveManifest() ArchiveManifest {
	return ArchiveManifest{
		Format:   ArchiveFormatVersion,
		PackKey:  "platform-agent-pack",
		Provider: "claude-code",
		Version:  "1.0.0",
		Files: map[string]string{
			"skills/b/SKILL.md": strings.Repeat("b", 64),
			"WORKFLOW.md":       strings.Repeat("a", 64),
		},
	}
}

func TestCanonicalManifestSerializesSortedKeysWithoutWhitespace(t *testing.T) {
	raw, err := sampleArchiveManifest().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"files":{"WORKFLOW.md":"` + strings.Repeat("a", 64) + `","skills/b/SKILL.md":"` + strings.Repeat("b", 64) +
		`"},"format":2,"packKey":"platform-agent-pack","provider":"claude-code","version":"1.0.0"}`
	if string(raw) != want {
		t.Fatalf("canonical manifest = %s\nwant %s", raw, want)
	}
	parsed, err := ParseCanonicalManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.PackKey != "platform-agent-pack" || parsed.Provider != "claude-code" || parsed.Version != "1.0.0" || len(parsed.Files) != 2 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestCanonicalManifestRejectsInvalidFieldsOnSerialization(t *testing.T) {
	cases := map[string]func(*ArchiveManifest){
		"format":          func(m *ArchiveManifest) { m.Format = 1 },
		"pack key":        func(m *ArchiveManifest) { m.PackKey = "Platform" },
		"long pack key":   func(m *ArchiveManifest) { m.PackKey = strings.Repeat("a", 129) },
		"provider":        func(m *ArchiveManifest) { m.Provider = "claude" },
		"version":         func(m *ArchiveManifest) { m.Version = "1.0" },
		"prerelease":      func(m *ArchiveManifest) { m.Version = "1.0.0-beta" },
		"leading zero":    func(m *ArchiveManifest) { m.Version = "01.0.0" },
		"plus sign":       func(m *ArchiveManifest) { m.Version = "+1.0.0" },
		"large component": func(m *ArchiveManifest) { m.Version = "2147483648.0.0" },
		"no files":        func(m *ArchiveManifest) { m.Files = map[string]string{} },
		"uppercase hex":   func(m *ArchiveManifest) { m.Files["WORKFLOW.md"] = strings.Repeat("A", 64) },
		"short hex":       func(m *ArchiveManifest) { m.Files["WORKFLOW.md"] = "abc" },
		"reserved path":   func(m *ArchiveManifest) { m.Files["agent-pack.json"] = strings.Repeat("a", 64) },
		"legacy path":     func(m *ArchiveManifest) { m.Files["pack.yaml"] = strings.Repeat("a", 64) },
		"unsafe path":     func(m *ArchiveManifest) { m.Files["../x"] = strings.Repeat("a", 64) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := sampleArchiveManifest()
			manifest.Files = map[string]string{"WORKFLOW.md": strings.Repeat("a", 64)}
			mutate(&manifest)
			if _, err := manifest.CanonicalJSON(); err == nil {
				t.Fatal("invalid manifest serialized")
			}
		})
	}
	many := sampleArchiveManifest()
	many.Files = map[string]string{}
	for index := 0; index <= ArchiveMaxFiles; index++ {
		many.Files[fmt.Sprintf("f%04d", index)] = strings.Repeat("a", 64)
	}
	if _, err := many.CanonicalJSON(); err == nil {
		t.Fatal("manifest with too many files serialized")
	}
}

func TestParseCanonicalManifestClassifiesRejections(t *testing.T) {
	valid, err := sampleArchiveManifest().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	text := string(valid)
	cases := map[string]struct {
		raw    string
		reason ArchiveRejectionReason
	}{
		"whitespace":     {strings.Replace(text, `"format":2`, `"format": 2`, 1), RejectManifestNoncanonical},
		"trailing nl":    {text + "\n", RejectManifestNoncanonical},
		"bom":            {"\xef\xbb\xbf" + text, RejectManifestNoncanonical},
		"key order":      {strings.Replace(text, `"packKey":"platform-agent-pack","provider":"claude-code"`, `"provider":"claude-code","packKey":"platform-agent-pack"`, 1), RejectManifestNoncanonical},
		"unknown key":    {strings.Replace(text, `"version"`, `"extra":"x","version"`, 1), RejectManifestNoncanonical},
		"duplicate key":  {strings.Replace(text, `"format":2`, `"format":2,"format":2`, 1), RejectManifestNoncanonical},
		"uppercase hex":  {strings.Replace(text, strings.Repeat("a", 64), strings.Repeat("A", 64), 1), RejectManifestNoncanonical},
		"invalid json":   {text[:len(text)-1], RejectManifestNoncanonical},
		"not object":     {`[]`, RejectManifestNoncanonical},
		"format string":  {strings.Replace(text, `"format":2`, `"format":"2"`, 1), RejectManifestNoncanonical},
		"format float":   {strings.Replace(text, `"format":2`, `"format":2.0`, 1), RejectManifestNoncanonical},
		"missing format": {strings.Replace(text, `"format":2,`, ``, 1), RejectManifestNoncanonical},
		"format 3":       {strings.Replace(text, `"format":2`, `"format":3`, 1), RejectUnsupportedFormat},
		"format 1":       {strings.Replace(text, `"format":2`, `"format":1`, 1), RejectUnsupportedFormat},
		"provider":       {strings.Replace(text, `"claude-code"`, `"claude"`, 1), RejectManifestNoncanonical},
		"escaped key":    {strings.Replace(text, `"version"`, `"vers`+"\\"+`u0069on"`, 1), RejectManifestNoncanonical},
		"files array":    {strings.Replace(text, `{"WORKFLOW.md"`, `[{"WORKFLOW.md"`, 1), RejectManifestNoncanonical},
		"invalid utf8":   {strings.Replace(text, "1.0.0", "1.0.\xff", 1), RejectManifestNoncanonical},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCanonicalManifest([]byte(test.raw))
			reason, ok := ArchiveRejectionReasonOf(err)
			if !ok || reason != test.reason {
				t.Fatalf("reason = %q %v (%v), want %q", reason, ok, err, test.reason)
			}
		})
	}
}

func TestValidArchivePathRules(t *testing.T) {
	valid := []string{
		"a", "WORKFLOW.md", ".claude-plugin/plugin.json", "a/b/c", strings.Repeat("a/", 15) + "z", "x_y-z.0",
		"constructor", "prototype/constructor", "toString", "a/__proto__x", "__proto", "docs/agent-pack.json.md",
		".gitignore", ".github/workflow.yml", "docs/.GITIGNORE", "a.DS_Store", "x._y", "__PROTO__x", "AGENT-PACK.JSON.md", "thumbs.db.md",
	}
	for _, name := range valid {
		if !validArchivePath(name) {
			t.Errorf("valid path %q rejected", name)
		}
	}
	invalid := []string{
		"", "/a", "a/", "a//b", "./a", "a/./b", "a/../b", "..", ".", "a b", "café", "a\\b", "a:b",
		strings.Repeat("a/", 16) + "z", strings.Repeat("a", 256), "agent-pack.json", "pack.yaml", "signature",
		"__proto__", "a/__proto__", "__proto__/x.md", "sub/agent-pack.json", "agent-pack.json/x", "a/agent-pack.json/b",
		"AGENT-PACK.JSON", "sub/AGENT-PACK.JSON", "a/Agent-Pack.Json/b", "__PROTO__", "a/__Proto__/b",
		".git", ".GIT", ".git/config", "a/.Git/HEAD", ".DS_Store", ".ds_store", "a/.DS_STORE", "Thumbs.db", "a/THUMBS.DB", "._x", "a/._SKILL.md",
	}
	for _, name := range invalid {
		if validArchivePath(name) {
			t.Errorf("invalid path %q accepted", name)
		}
	}
	if !validArchivePath("docs/signature") || !validArchivePath("docs/pack.yaml") {
		t.Fatal("reserved names are only reserved at the archive root")
	}
}

func TestParseCanonicalManifestMatchesTypeScriptEdgeClassifications(t *testing.T) {
	canonical := `{"files":{"A.md":"` + strings.Repeat("a", 64) + `","b.md":"` + strings.Repeat("b", 64) + `"},"format":2,"packKey":"fixture","provider":"claude-code","version":"1.0.0"}`
	if _, err := ParseCanonicalManifest([]byte(canonical)); err != nil {
		t.Fatal(err)
	}
	escapedDot := `"A` + "\\" + `u002emd"`
	cases := []struct {
		name   string
		raw    string
		reason ArchiveRejectionReason
	}{
		{"null", "null", RejectManifestNoncanonical},
		{"array", "[]", RejectManifestNoncanonical},
		{"byte order mark", "\xef\xbb\xbf" + canonical, RejectManifestNoncanonical},
		{"non-finite format", strings.Replace(canonical, `"format":2`, `"format":2e400`, 1), RejectManifestNoncanonical},
		{"string format", strings.Replace(canonical, `"format":2`, `"format":"2"`, 1), RejectManifestNoncanonical},
		{"finite other format", strings.Replace(canonical, `"format":2`, `"format":1e300`, 1), RejectUnsupportedFormat},
		{"negative zero format", strings.Replace(canonical, `"format":2`, `"format":-0`, 1), RejectUnsupportedFormat},
		{"float spelling of two", strings.Replace(canonical, `"format":2`, `"format":2.0`, 1), RejectManifestNoncanonical},
		{"proto key", strings.Replace(canonical, `{"files"`, `{"__proto__":{},"files"`, 1), RejectManifestNoncanonical},
		{"escaped path", strings.Replace(canonical, `"A.md"`, escapedDot, 1), RejectManifestNoncanonical},
		{"reserved path", strings.Replace(canonical, `"A.md"`, `"signature"`, 1), RejectManifestNoncanonical},
		{"provider alias", strings.Replace(canonical, "claude-code", "claude", 1), RejectManifestNoncanonical},
		{"oversized version component", strings.Replace(canonical, "1.0.0", "2147483648.0.0", 1), RejectManifestNoncanonical},
		{"invalid utf-8", strings.Replace(canonical, "fixture", "\xffixture", 1), RejectManifestNoncanonical},
		{"nesting over the bound with format 3", nestedManifest(canonical, 3, archiveManifestMaxNesting), RejectManifestNoncanonical},
		{"nesting at the bound with format 3", nestedManifest(canonical, 3, archiveManifestMaxNesting-1), RejectUnsupportedFormat},
		{"nesting beyond the JSON decoder limit with format 3", nestedManifest(canonical, 3, 10001), RejectManifestNoncanonical},
		{"nesting over the bound with format 2", nestedManifest(canonical, 2, archiveManifestMaxNesting), RejectManifestNoncanonical},
		{"brackets inside strings do not nest", strings.Replace(canonical, `"format":2`, `"format":3,"x":"`+strings.Repeat("[{", 100)+`\"`+`"`, 1), RejectUnsupportedFormat},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseCanonicalManifest([]byte(testCase.raw))
			if reason, ok := ArchiveRejectionReasonOf(err); !ok || reason != testCase.reason {
				t.Fatalf("reason = %q %v, want %q", reason, ok, testCase.reason)
			}
		})
	}
}

func nestedManifest(canonical string, format, depth int) string {
	value := strings.Repeat("[", depth) + strings.Repeat("]", depth)
	return strings.Replace(canonical, `"format":2`, fmt.Sprintf(`"format":%d,"nested":%s`, format, value), 1)
}

func TestManifestNestingCountsOnlyStructuralBrackets(t *testing.T) {
	cases := map[string]int{
		`{}`:                   1,
		`{"a":[{"b":[]}]}`:     4,
		`{"a":"[[[[{{{{"}`:     1,
		`{"a":"\"[[[","b":[]}`: 2,
		`{"a":"\\","b":[[]]}`:  3,
		`]]]]{`:                1,
	}
	for raw, want := range cases {
		if got := manifestNestingDepth([]byte(raw)); got != want {
			t.Errorf("depth(%s) = %d, want %d", raw, got, want)
		}
	}
}
