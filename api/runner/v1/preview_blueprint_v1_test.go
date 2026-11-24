package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

const previewBlueprintV1VectorsSHA256 = "5934ffc7661058d05532fb5195bdf43ef98c00857349964c0df548b65fb0316b"

type previewBlueprintV1VectorFile struct {
	Valid []struct {
		Name        string `json:"name"`
		ContentJSON string `json:"content_json"`
		Canonical   string `json:"canonical"`
		Digest      string `json:"digest"`
	} `json:"valid"`
	Invalid []struct {
		Name        string     `json:"name"`
		Rule        string     `json:"rule"`
		ContentJSON string     `json:"content_json"`
		Paths       [][]string `json:"paths"`
	} `json:"invalid"`
}

func readPreviewBlueprintV1Vectors(t *testing.T) previewBlueprintV1VectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/preview_blueprint_v1_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors previewBlueprintV1VectorFile
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func TestPreviewBlueprintV1VectorsAreAByteForByteCopyOfTheAppVectorFile(t *testing.T) {
	raw, err := os.ReadFile("testdata/preview_blueprint_v1_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != previewBlueprintV1VectorsSHA256 {
		t.Fatalf("vector file sha256 = %s, want %s; the app file changed, which is a contract change", got, previewBlueprintV1VectorsSHA256)
	}
	vectors := readPreviewBlueprintV1Vectors(t)
	if len(vectors.Valid) != 9 || len(vectors.Invalid) != 82 {
		t.Fatalf("vector counts = %d valid, %d invalid", len(vectors.Valid), len(vectors.Invalid))
	}
	if vectors.Valid[0].Digest != "83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563" {
		t.Fatalf("base digest = %s", vectors.Valid[0].Digest)
	}
}

func TestPreviewBlueprintV1ValidVectorsProduceTheirCanonicalJSONAndDigest(t *testing.T) {
	for _, vector := range readPreviewBlueprintV1Vectors(t).Valid {
		t.Run(vector.Name, func(t *testing.T) {
			raw := []byte(vector.ContentJSON)
			if _, err := ParsePreviewBlueprintContentV1(raw); err != nil {
				t.Fatalf("parse: %v", err)
			}
			canonical, err := PreviewBlueprintCanonicalJSONV1(raw)
			if err != nil {
				t.Fatalf("canonical: %v", err)
			}
			if string(canonical) != vector.Canonical {
				t.Fatalf("canonical =\n%s\nwant\n%s", canonical, vector.Canonical)
			}
			digest, err := PreviewBlueprintDigestV1(raw)
			if err != nil {
				t.Fatalf("digest: %v", err)
			}
			if digest != vector.Digest {
				t.Fatalf("digest = %s, want %s", digest, vector.Digest)
			}
		})
	}
}

func TestPreviewBlueprintV1ParseDecodesTheBaseVectorIntoTypedContent(t *testing.T) {
	content, err := ParsePreviewBlueprintContentV1([]byte(readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON))
	if err != nil {
		t.Fatal(err)
	}
	note := "Test mode key"
	want := PreviewBlueprintContentV1{
		SchemaVersion: 1,
		Services: []PreviewBlueprintServiceV1{{
			Name:  "db",
			Role:  "database",
			Image: "docker.io/library/postgres@sha256:" + strings.Repeat("a", 64),
			Env: map[string]PreviewBlueprintEnvEntryV1{
				"POSTGRES_PASSWORD": {Kind: PreviewBlueprintEnvGenerated, Generator: "random_secret"},
			},
		}},
		App: PreviewBlueprintAppV1{
			Image:            "ghcr.io/acme/node@sha256:" + strings.Repeat("b", 64),
			WorkingDirectory: ".",
			Install:          []string{"pnpm install --frozen-lockfile"},
			Build:            []string{"pnpm build"},
			Migrate:          []string{"pnpm db:migrate:deploy"},
			Seed:             []string{"pnpm db:seed"},
			Start:            "pnpm start",
			Port:             3000,
			HealthChecks:     []PreviewBlueprintHealthCheckV1{{ID: "home", Path: "/", ExpectedStatus: 200}},
			Env: map[string]PreviewBlueprintEnvEntryV1{
				"DATABASE_URL":      {Kind: PreviewBlueprintEnvGenerated, Generator: "postgres_url"},
				"STRIPE_SECRET_KEY": {Kind: PreviewBlueprintEnvSecret, Note: &note},
				"FEATURE_FLAG":      {Kind: PreviewBlueprintEnvFake, Value: "enabled"},
				"SENTRY_DSN":        {Kind: PreviewBlueprintEnvDisabled},
			},
		},
		Limits: PreviewBlueprintLimitsV1{MemoryMB: 1024, CPUs: 1, MaxLifetimeMinutes: 1440},
		Egress: PreviewBlueprintEgressV1{Mode: PreviewBlueprintEgressNone},
	}
	if !reflect.DeepEqual(content, want) {
		t.Fatalf("content =\n%#v\nwant\n%#v", content, want)
	}
}

func TestPreviewBlueprintV1ParseRefusesStructuralVectorsAtTheAppIssuePath(t *testing.T) {
	structural := map[string]bool{
		"unknown_top_level_key":        true,
		"unknown_app_key":              true,
		"missing_required_key":         true,
		"env_kind_enum":                true,
		"port_not_integer":             true,
		"memory_not_integer":           true,
		"egress_mode_enum":             true,
		"egress_none_with_hosts":       true,
		"wrong_type_string_for_number": true,
	}
	seen := 0
	for _, vector := range readPreviewBlueprintV1Vectors(t).Invalid {
		if !structural[vector.Name] {
			continue
		}
		seen++
		t.Run(vector.Name, func(t *testing.T) {
			_, err := ParsePreviewBlueprintContentV1([]byte(vector.ContentJSON))
			var contentErr *PreviewBlueprintContentErrorV1
			if !errors.As(err, &contentErr) {
				t.Fatalf("err = %v, want a content error", err)
			}
			if got := contentErr.Paths(); !reflect.DeepEqual(got, vector.Paths) {
				t.Fatalf("paths = %q, want %q (%v)", got, vector.Paths, err)
			}
			if _, err := PreviewBlueprintCanonicalJSONV1([]byte(vector.ContentJSON)); err == nil {
				t.Fatal("canonical accepted structurally invalid content")
			}
		})
	}
	if seen != len(structural) {
		t.Fatalf("matched %d structural vectors, want %d", seen, len(structural))
	}
}

func TestPreviewBlueprintV1ParseRefusesMalformedJSONBeforeStructure(t *testing.T) {
	base := readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON
	for name, raw := range map[string][]byte{
		"invalid utf-8":         []byte(strings.Replace(base, "pnpm start", "pnpm \xff", 1)),
		"trailing value":        []byte(base + " {}"),
		"trailing garbage":      []byte(base + "x"),
		"byte order mark":       append([]byte("\xef\xbb\xbf"), base...),
		"empty":                 []byte(""),
		"number overflows":      []byte(strings.Replace(base, `"port":3000`, `"port":1e400`, 1)),
		"top level not object":  []byte(`[]`),
		"top level null":        []byte(`null`),
		"two documents":         []byte(base + base),
		"unterminated":          []byte(base[:len(base)-1]),
		"leading zero number":   []byte(strings.Replace(base, `"port":3000`, `"port":03000`, 1)),
		"single quoted string":  []byte(strings.Replace(base, `"pnpm start"`, `'pnpm start'`, 1)),
		"raw control in string": []byte(strings.Replace(base, "pnpm start", "pnpm\tstart", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePreviewBlueprintContentV1(raw); err == nil {
				t.Fatal("parse accepted malformed JSON")
			}
			if _, err := PreviewBlueprintDigestV1(raw); err == nil {
				t.Fatal("digest accepted malformed JSON")
			}
		})
	}
}

func TestPreviewBlueprintV1PairedSurrogateEscapesDecodeToTheCodePoint(t *testing.T) {
	base := readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON
	escaped, err := PreviewBlueprintCanonicalJSONV1([]byte(strings.Replace(base, "pnpm start", "pnpm "+jsonUnicodeEscape("d83d")+jsonUnicodeEscape("de00"), 1)))
	if err != nil {
		t.Fatal(err)
	}
	literal, err := PreviewBlueprintCanonicalJSONV1([]byte(strings.Replace(base, "pnpm start", "pnpm 😀", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if string(escaped) != string(literal) || !strings.Contains(string(literal), `"start":"pnpm 😀"`) {
		t.Fatalf("escaped = %s\nliteral = %s", escaped, literal)
	}
}

func jsonUnicodeEscape(hex string) string {
	return "\\" + "u" + hex
}

func TestPreviewBlueprintV1LoneSurrogateEscapesSurviveDecodingLikeJSONParse(t *testing.T) {
	base := readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON
	for input, want := range map[string]string{
		jsonUnicodeEscape("d800"):                                                         jsonUnicodeEscape("d800"),
		jsonUnicodeEscape("DC00") + "x":                                                   jsonUnicodeEscape("dc00") + "x",
		jsonUnicodeEscape("de00") + jsonUnicodeEscape("d83d"):                             jsonUnicodeEscape("de00") + jsonUnicodeEscape("d83d"),
		jsonUnicodeEscape("d83d") + jsonUnicodeEscape("d83d") + jsonUnicodeEscape("de00"): jsonUnicodeEscape("d83d") + "😀",
	} {
		t.Run(input, func(t *testing.T) {
			raw := []byte(strings.Replace(base, "pnpm start", "pnpm "+input, 1))
			content, err := ParsePreviewBlueprintContentV1(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if utf8.ValidString(content.App.Start) || strings.Contains(content.App.Start, string(utf8.RuneError)) {
				t.Fatalf("start = %q, want the lone surrogate kept as an invalid sequence", content.App.Start)
			}
			canonical, err := PreviewBlueprintCanonicalJSONV1(raw)
			if err != nil {
				t.Fatalf("canonical: %v", err)
			}
			if !strings.Contains(string(canonical), `"start":"pnpm `+want+`"`) {
				t.Fatalf("canonical = %s, want start %s", canonical, want)
			}
		})
	}
	content, err := ParsePreviewBlueprintContentV1([]byte(strings.Replace(base, "pnpm start", "pnpm "+jsonUnicodeEscape("fffd"), 1)))
	if err != nil || content.App.Start != "pnpm �" {
		t.Fatalf("start = %q, %v", content.App.Start, err)
	}
}

func TestPreviewBlueprintV1ParseRefusesReservedEnvKeysAtTheEnvMap(t *testing.T) {
	base := readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON
	for _, key := range []string{"__proto__", "constructor", "prototype"} {
		for _, entry := range []string{`{"kind":"disabled"}`, `1`, `{"kind":"nope"}`} {
			for _, test := range []struct {
				old, new string
				path     []string
			}{
				{`"SENTRY_DSN":{"kind":"disabled"}`, `"` + key + `":` + entry, []string{"app", "env"}},
				{`"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}`, `"` + key + `":` + entry, []string{"services", "0", "env"}},
			} {
				raw := strings.Replace(base, test.old, test.new, 1)
				_, err := ParsePreviewBlueprintContentV1([]byte(raw))
				var contentErr *PreviewBlueprintContentErrorV1
				if !errors.As(err, &contentErr) {
					t.Fatalf("%s %s: err = %v, want a content error", key, entry, err)
				}
				if got := contentErr.Paths(); !reflect.DeepEqual(got, [][]string{test.path}) {
					t.Fatalf("%s %s: paths = %q, want %q", key, entry, got, test.path)
				}
			}
		}
	}
}

func TestPreviewBlueprintV1DuplicateKeysKeepTheLastValueLikeJSONParse(t *testing.T) {
	base := readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON
	duplicated := strings.Replace(base, `"port":3000`, `"port":1,"port":3000`, 1)
	content, err := ParsePreviewBlueprintContentV1([]byte(duplicated))
	if err != nil {
		t.Fatal(err)
	}
	if content.App.Port != 3000 {
		t.Fatalf("port = %d, want the last duplicate 3000", content.App.Port)
	}
	digest, err := PreviewBlueprintDigestV1([]byte(duplicated))
	if err != nil {
		t.Fatal(err)
	}
	if digest != "83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563" {
		t.Fatalf("digest = %s, want the base digest", digest)
	}
}

func TestPreviewBlueprintV1ParseMatchesKeysExactlyNotCaseInsensitively(t *testing.T) {
	base := readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON
	_, err := ParsePreviewBlueprintContentV1([]byte(strings.Replace(base, `"port":3000`, `"Port":3000`, 1)))
	var contentErr *PreviewBlueprintContentErrorV1
	if !errors.As(err, &contentErr) {
		t.Fatalf("err = %v", err)
	}
	if got, want := contentErr.Paths(), [][]string{{"app", "port"}, {"app"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestPreviewBlueprintV1ParseRefusesNullAndWrongTypes(t *testing.T) {
	base := readPreviewBlueprintV1Vectors(t).Valid[0].ContentJSON
	for name, test := range map[string]struct {
		old, new string
		path     []string
	}{
		"null env":              {`"env":{"DATABASE_URL"`, `"env":null,"x_env":{"DATABASE_URL"`, nil},
		"null service env":      {`"env":{"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}}`, `"env":null`, []string{"services", "0", "env"}},
		"string for array":      {`"install":["pnpm install --frozen-lockfile"]`, `"install":"pnpm install"`, []string{"app", "install"}},
		"number in command":     {`"build":["pnpm build"]`, `"build":[1]`, []string{"app", "build", "0"}},
		"bool for cpus":         {`"cpus":1`, `"cpus":true`, []string{"limits", "cpus"}},
		"missing kind":          {`{"kind":"disabled"}`, `{}`, []string{"app", "env", "SENTRY_DSN", "kind"}},
		"numeric kind":          {`{"kind":"disabled"}`, `{"kind":1}`, []string{"app", "env", "SENTRY_DSN", "kind"}},
		"fake without value":    {`{"kind":"fake","value":"enabled"}`, `{"kind":"fake"}`, []string{"app", "env", "FEATURE_FLAG", "value"}},
		"null note":             {`"note":"Test mode key"`, `"note":null`, []string{"app", "env", "STRIPE_SECRET_KEY", "note"}},
		"schema version string": {`"schema_version":1`, `"schema_version":"1"`, []string{"schema_version"}},
		"unsafe integer":        {`"port":3000`, `"port":9007199254740993`, []string{"app", "port"}},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(base, test.old, test.new, 1)
			if raw == base {
				t.Fatal("replacement did not apply")
			}
			_, err := ParsePreviewBlueprintContentV1([]byte(raw))
			var contentErr *PreviewBlueprintContentErrorV1
			if !errors.As(err, &contentErr) {
				t.Fatalf("err = %v, want a content error", err)
			}
			if test.path == nil {
				return
			}
			found := false
			for _, path := range contentErr.Paths() {
				if reflect.DeepEqual(path, test.path) {
					found = true
				}
			}
			if !found {
				t.Fatalf("paths = %q, want %q", contentErr.Paths(), test.path)
			}
		})
	}
}

func TestPreviewBlueprintCanonicalJSONEscapesStringsLikeJSONStringify(t *testing.T) {
	for input, want := range map[string]string{
		"<>&":                                `"<>&"`,
		"  ":                                 "\"  \"",
		"\"\\/":                              `"\"\\/"`,
		"\b\f\n\r\t":                         `"\b\f\n\r\t"`,
		"\x00\x01\x1f":                       `"\u0000\u0001\u001f"`,
		"\x0b\x1b":                           `"\u000b\u001b"`,
		"\x7f":                               "\"\x7f\"",
		"Zürich – 日本 😀":                      `"Zürich – 日本 😀"`,
		string([]rune{0xa0, 0xfeff, 0xffff}): `"` + string([]rune{0xa0, 0xfeff, 0xffff}) + `"`,
		"":                                   `""`,
		"a\u0080b\u009f":                     "\"a\u0080b\u009f\"",
		"\U0010ffff\U00010000":               "\"\U0010ffff\U00010000\"",
	} {
		var out strings.Builder
		if err := writePreviewBlueprintCanonicalJSON(&out, input); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Errorf("canonical(%q) = %s, want %s", input, out.String(), want)
		}
	}
}

func TestPreviewBlueprintCanonicalJSONFormatsNumbersLikeECMAScript(t *testing.T) {
	for input, want := range map[string]string{
		"1":                      "1",
		"1.0":                    "1",
		"1e0":                    "1",
		"1.024e3":                "1024",
		"0.25":                   "0.25",
		"1.5":                    "1.5",
		"-1.5":                   "-1.5",
		"-0":                     "0",
		"0.0":                    "0",
		"-0.0e5":                 "0",
		"100":                    "100",
		"1e20":                   "100000000000000000000",
		"123456789012345680000":  "123456789012345680000",
		"1e21":                   "1e+21",
		"1.5e21":                 "1.5e+21",
		"999999999999999999999":  "1e+21",
		"1e-6":                   "0.000001",
		"1.5e-6":                 "0.0000015",
		"1e-7":                   "1e-7",
		"1.23e-7":                "1.23e-7",
		"123e-20":                "1.23e-18",
		"0.1":                    "0.1",
		"0.30000000000000004":    "0.30000000000000004",
		"5e-324":                 "5e-324",
		"1.7976931348623157e308": "1.7976931348623157e+308",
		"-1e-7":                  "-1e-7",
		"9007199254740993":       "9007199254740992",
		"4.35":                   "4.35",
		"1e-400":                 "0",
		"12345.678":              "12345.678",
		"0.000001234":            "0.000001234",
		"1E+2":                   "100",
	} {
		var out strings.Builder
		if err := writePreviewBlueprintCanonicalJSON(&out, json.Number(input)); err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if out.String() != want {
			t.Errorf("canonical(%s) = %s, want %s", input, out.String(), want)
		}
	}
	for _, input := range []string{"1e400", "-1e400"} {
		var out strings.Builder
		if err := writePreviewBlueprintCanonicalJSON(&out, json.Number(input)); err == nil {
			t.Errorf("canonical(%s) accepted a non-finite number: %s", input, out.String())
		}
	}
}

func TestPreviewBlueprintCanonicalJSONFormatsNumbersLikeECMAScriptAcrossRandomDoubles(t *testing.T) {
	for _, value := range []float64{math.SmallestNonzeroFloat64, math.MaxFloat64, 1 / 3.0, 2.0 / 3.0, 1e21 - 65536, 1e-6 * 0.999, math.Pi * 1e100} {
		var out strings.Builder
		if err := writePreviewBlueprintCanonicalJSON(&out, json.Number(formatTestFloat(value))); err != nil {
			t.Fatal(err)
		}
		if back := mustParseTestFloat(t, out.String()); back != value {
			t.Errorf("canonical(%v) = %s does not round-trip", value, out.String())
		}
	}
}

func TestPreviewBlueprintCanonicalJSONSortsKeysByUTF16CodeUnits(t *testing.T) {
	value := map[string]any{
		"b":          json.Number("1"),
		"a":          []any{true, false, nil, "x"},
		"\U0001F600": "astral",
		"｡":          "bmp-high",
		"B":          map[string]any{"z": json.Number("2"), "y": map[string]any{}},
		"aa":         []any{},
	}
	var out strings.Builder
	if err := writePreviewBlueprintCanonicalJSON(&out, value); err != nil {
		t.Fatal(err)
	}
	want := `{"B":{"y":{},"z":2},"a":[true,false,null,"x"],"aa":[],"b":1,"` + "\U0001F600" + `":"astral","` + "｡" + `":"bmp-high"}`
	if out.String() != want {
		t.Fatalf("canonical = %s\nwant      %s", out.String(), want)
	}
}

func formatTestFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func mustParseTestFloat(t *testing.T, text string) float64 {
	t.Helper()
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
