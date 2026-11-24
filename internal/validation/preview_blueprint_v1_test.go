package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type previewBlueprintV1Vectors struct {
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

func readPreviewBlueprintV1TestVectors(t *testing.T) previewBlueprintV1Vectors {
	t.Helper()
	raw, err := os.ReadFile("../../api/runner/v1/testdata/preview_blueprint_v1_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors previewBlueprintV1Vectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func jsonText(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func previewBlueprintBaseJSON(t *testing.T) string {
	t.Helper()
	return readPreviewBlueprintV1TestVectors(t).Valid[0].ContentJSON
}

func sortedPreviewBlueprintPaths(paths [][]string) []string {
	joined := make([]string, len(paths))
	for index, path := range paths {
		encoded, _ := json.Marshal(path)
		joined[index] = string(encoded)
	}
	slices.Sort(joined)
	return joined
}

func previewBlueprintIssuePaths(t *testing.T, raw string) []string {
	t.Helper()
	_, err := ParsePreviewBlueprintContentV1([]byte(raw))
	var contentErr *v1.PreviewBlueprintContentErrorV1
	if !errors.As(err, &contentErr) {
		t.Fatalf("err = %v, want a content error", err)
	}
	return sortedPreviewBlueprintPaths(contentErr.Paths())
}

func TestPreviewBlueprintV1AcceptsEveryValidVector(t *testing.T) {
	for _, vector := range readPreviewBlueprintV1TestVectors(t).Valid {
		t.Run(vector.Name, func(t *testing.T) {
			content, err := ParsePreviewBlueprintContentV1([]byte(vector.ContentJSON))
			if err != nil {
				t.Fatal(err)
			}
			if content.SchemaVersion != 1 {
				t.Fatalf("schema_version = %d", content.SchemaVersion)
			}
			digest, err := v1.PreviewBlueprintDigestV1([]byte(vector.ContentJSON))
			if err != nil || digest != vector.Digest {
				t.Fatalf("digest = %s, %v; want %s", digest, err, vector.Digest)
			}
		})
	}
}

func TestPreviewBlueprintV1RefusesEveryInvalidVectorAtTheAppIssuePaths(t *testing.T) {
	vectors := readPreviewBlueprintV1TestVectors(t).Invalid
	if len(vectors) != 82 {
		t.Fatalf("invalid vectors = %d", len(vectors))
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			got := previewBlueprintIssuePaths(t, vector.ContentJSON)
			if want := sortedPreviewBlueprintPaths(vector.Paths); !reflect.DeepEqual(got, want) {
				t.Fatalf("paths = %v, want %v (rule: %s)", got, want, vector.Rule)
			}
		})
	}
}

func TestPreviewBlueprintV1ErrorNamesTheIssuePathAndMessage(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	_, err := ParsePreviewBlueprintContentV1([]byte(strings.Replace(base, `"port":3000`, `"port":80`, 1)))
	if err == nil || !strings.Contains(err.Error(), "app.port") {
		t.Fatalf("err = %v", err)
	}
}

func TestPreviewBlueprintV1RefusesEveryLiveCredentialPrefix(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for _, value := range []string{
		"sk_live_x",
		"rk_live_x",
		"AKIAABCDEFGHIJKL",
		"ghp_" + strings.Repeat("a", 20),
		"gho_" + strings.Repeat("a", 20),
		"ghu_" + strings.Repeat("a", 20),
		"ghs_" + strings.Repeat("a", 20),
		"ghr_" + strings.Repeat("a", 20),
		"github_pat_x",
		"glpat-x",
		"whsec_x",
		"xoxa-1",
		"xoxb-1",
		"xoxe-1",
		"xoxp-1",
		"xoxr-1",
		"xoxs-1",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		"redis://user:pw@cache:6379",
		"HTTPS://User:Pw@example.com",
		"git+ssh://u:p@host/repo",
		"1http://user:pw@example.com",
		"redis://:secret@cache:6379",
		"https://api.example.com/?password=hunter2",
		"https://api.example.com/?a=1&PWD=x",
		"db;pass=x",
		"DB_PASSWORD=hunter2",
		"export db_passwd=x",
		"PASSWORD=x$",
		`PGPASSWORD="hunter2"`,
		"PGPASSWORD='hunter2'",
		"ſecret_PASSWORD=x",
	} {
		t.Run(value, func(t *testing.T) {
			raw := strings.Replace(base, `"value":"enabled"`, `"value":`+jsonText(value), 1)
			got := previewBlueprintIssuePaths(t, raw)
			if want := []string{`["app","env","FEATURE_FLAG","value"]`}; !reflect.DeepEqual(got, want) {
				t.Fatalf("paths = %v", got)
			}
		})
	}
}

func TestPreviewBlueprintV1AcceptsTextThatOnlyResemblesACredential(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for _, value := range []string{
		"AKIAABCDEFGHIJK",
		"ghp_" + strings.Repeat("a", 19),
		"ghx_" + strings.Repeat("a", 30),
		"xoxz-1",
		"https://user@example.com",
		"https://user:@example.com",
		"https://:@example.com",
		"https://api.example.com/?password=$PASSWORD",
		"https://api.example.com/?password=",
		"https://api.example.com/?passwords=x",
		"DB_PASSWORD=$DB_PASSWORD",
		`PGPASSWORD="$PGPASSWORD"`,
		"PGPASSWORD='$PGPASSWORD'",
		"PASSWORD=",
		`PASSWORD=""`,
		"PASSWORD= x",
		"PASSWORDS=x",
		"https://us er:pw@example.com",
		"https://user:p w@example.com",
		"https://user:pw @example.com",
		strings.Repeat("aB3", 11),
		"abcdefghijklmnopqrstuvwxyz012345",
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ012345",
		"Zx9Qm2Lp7Rt4Vw8Ny3Kb6Hd1Fg5Js0A",
		"Zx9Qm2Lp7Rt4.Vw8Ny3Kb6Hd1Fg5Js0Ac",
	} {
		t.Run(value, func(t *testing.T) {
			raw := strings.Replace(base, `"value":"enabled"`, `"value":`+jsonText(value), 1)
			if _, err := ParsePreviewBlueprintContentV1([]byte(raw)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreviewBlueprintV1RefusesHighEntropyTokensInsideLongerText(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for _, value := range []string{
		"token=Zx9Qm2Lp7Rt4Vw8Ny3Kb6Hd1Fg5Js0Ac;",
		"Zx9Qm2Lp7Rt4/Vw8Ny3Kb6Hd1+Fg5Js0Ac==",
		"a b Zx9Qm2Lp7Rt4_Vw8Ny3Kb6Hd1-Fg5Js0Ac",
		"0123456789abcdefABCDEF0123456789",
	} {
		t.Run(value, func(t *testing.T) {
			raw := strings.Replace(base, `"pnpm start"`, jsonText("pnpm start "+value), 1)
			if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{`["app","start"]`}) {
				t.Fatalf("paths = %v", got)
			}
		})
	}
}

func TestPreviewBlueprintV1CountsStringLimitsInCodePointsLikeZod4AndCommandsInUTF8Bytes(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	replace := func(value string) string {
		return strings.Replace(base, `"note":"Test mode key"`, `"note":`+jsonText(value), 1)
	}
	if _, err := ParsePreviewBlueprintContentV1([]byte(replace(strings.Repeat("😀", 280)))); err != nil {
		t.Fatalf("280 astral code points (560 UTF-16 code units) are accepted by the app: %v", err)
	}
	if got := previewBlueprintIssuePaths(t, replace(strings.Repeat("😀", 281))); !reflect.DeepEqual(got, []string{`["app","env","STRIPE_SECRET_KEY","note"]`}) {
		t.Fatalf("paths = %v", got)
	}
	if _, err := ParsePreviewBlueprintContentV1([]byte(replace(strings.Repeat("é", 280)))); err != nil {
		t.Fatalf("280 two-byte characters: %v", err)
	}
	start := func(value string) string {
		return strings.Replace(base, `"pnpm start"`, jsonText(value), 1)
	}
	if _, err := ParsePreviewBlueprintContentV1([]byte(start(strings.Repeat("é", 1024)))); err != nil {
		t.Fatalf("2048 bytes: %v", err)
	}
	if got := previewBlueprintIssuePaths(t, start(strings.Repeat("é", 1024)+"a")); !reflect.DeepEqual(got, []string{`["app","start"]`}) {
		t.Fatalf("2049 bytes: paths = %v", got)
	}
	if got := previewBlueprintIssuePaths(t, start(strings.Repeat("a", 2049))); !reflect.DeepEqual(got, []string{`["app","start"]`, `["app","start"]`}) {
		t.Fatalf("2049 code units: paths = %v", got)
	}
}

func TestPreviewBlueprintV1HealthPathRules(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for path, ok := range map[string]bool{
		"/":                            true,
		"/api/health":                  true,
		"/a.b/c..d/%41":                true,
		"/a/":                          true,
		"/%2E":                         false,
		"/%2F":                         false,
		"/%5c":                         false,
		"/a#b":                         false,
		"/a b":                         false,
		"/a b":                         false,
		"/a　b":                         false,
		"/a\x7fb":                      false,
		"/..":                          false,
		"/a/../b":                      false,
		"//":                           false,
		"":                             false,
		"/" + strings.Repeat("a", 511): true,
		"/" + strings.Repeat("a", 512): false,
	} {
		raw := strings.Replace(base, `"path":"/"`, `"path":`+jsonText(path), 1)
		_, err := ParsePreviewBlueprintContentV1([]byte(raw))
		if (err == nil) != ok {
			t.Errorf("path %q: err = %v, want ok %v", path, err, ok)
		}
	}
}

func TestPreviewBlueprintV1RegistryHostRules(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	digest := "@sha256:" + strings.Repeat("b", 64)
	for repository, ok := range map[string]bool{
		"node":                           true,
		"acme/node":                      true,
		"ghcr.io/acme/node":              true,
		"registry.example.com:5000/node": true,
		"localhost/node":                 false,
		"localhost:5000/node":            false,
		"10.0.0.1/node":                  false,
		"10.0.0.1:5000/node":             false,
		"registry.local/node":            false,
		"registry.localdomain/node":      false,
		"registry.intranet/node":         false,
		"registry.lan/node":              false,
		"router.home.arpa/node":          false,
		"10.0.0.1.sslip.io/node":         false,
		"internal/node":                  true,
		"a.internal/node":                false,
		"example.internalx/node":         true,
		"registry.example/node":          false,
		"registry.example.com/node":      true,
		"postgres.default.svc/node":      false,
		"registry.corp/node":             false,
		"registry.test/node":             false,
		"registry.invalid/node":          false,
		"registry.onion/node":            false,
		"localtest.me/node":              false,
		"registry.localtest.me/node":     false,
		"lvh.me/node":                    false,
		"a.lvh.me:5000/node":             false,
		"notlvh.me/node":                 true,
		"svc.io/node":                    true,
		"registry.example1/node":         true,
		"registry.1example/node":         false,
		"registry:5000/node":             false,
	} {
		raw := strings.Replace(base, `"ghcr.io/acme/node@sha256:`+strings.Repeat("b", 64)+`"`, jsonText(repository+digest), 1)
		_, err := ParsePreviewBlueprintContentV1([]byte(raw))
		if (err == nil) != ok {
			t.Errorf("image %q: err = %v, want ok %v", repository, err, ok)
		}
	}
}

func TestPreviewBlueprintV1EnvKeysAndProtoKeyAreRefused(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for key, ok := range map[string]bool{
		"_":                            true,
		"A1":                           true,
		"A" + strings.Repeat("B", 127): true,
		"A" + strings.Repeat("B", 128): false,
		"1A":                           false,
		"A-B":                          false,
		"__proto__":                    false,
		"constructor":                  false,
		"prototype":                    false,
		"":                             false,
	} {
		raw := strings.Replace(base, `"SENTRY_DSN"`, jsonText(key), 1)
		_, err := ParsePreviewBlueprintContentV1([]byte(raw))
		if (err == nil) != ok {
			t.Errorf("key %q: err = %v, want ok %v", key, err, ok)
		}
	}
	for _, key := range []string{"__proto__", "constructor", "prototype"} {
		raw := strings.Replace(base, `"SENTRY_DSN":{"kind":"disabled"}`, jsonText(key)+`:{"kind":"disabled"}`, 1)
		if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{`["app","env"]`}) {
			t.Errorf("key %q: paths = %v", key, got)
		}
	}
}

func TestPreviewBlueprintV1RefusesLoneSurrogatesInTextAtTheFieldPath(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	escape := func(hex string) string { return "\\" + "u" + hex }
	for name, test := range map[string]struct {
		old, new string
		path     string
	}{
		"fake high":     {`"value":"enabled"`, `"value":"a` + escape("d800") + `b"`, `["app","env","FEATURE_FLAG","value"]`},
		"note low":      {`"note":"Test mode key"`, `"note":"` + escape("dc00") + `"`, `["app","env","STRIPE_SECRET_KEY","note"]`},
		"start reverse": {`"pnpm start"`, `"pnpm ` + escape("de00") + escape("d83d") + `"`, `["app","start"]`},
		"health path":   {`"path":"/"`, `"path":"/` + escape("dc00") + `"`, `["app","health_checks","0","path"]`},
		"working dir":   {`"working_directory":"."`, `"working_directory":"` + escape("d800") + `"`, `["app","working_directory"]`},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(base, test.old, test.new, 1)
			if raw == base {
				t.Fatal("replacement did not apply")
			}
			if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{test.path}) {
				t.Fatalf("paths = %v, want %s", got, test.path)
			}
		})
	}
	note := strings.Replace(base, `"note":"Test mode key"`, `"note":"`+strings.Repeat(escape("d800"), 280)+`"`, 1)
	_, err := ParsePreviewBlueprintContentV1([]byte(note))
	var contentErr *v1.PreviewBlueprintContentErrorV1
	if !errors.As(err, &contentErr) || len(contentErr.Issues) != 1 || contentErr.Issues[0].Message != "Text must be valid Unicode" {
		t.Fatalf("280 lone surrogates count as 280 code points: %v", err)
	}
}

func TestPreviewBlueprintV1GeneratorsNeedTheirServiceRoles(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for generator, path := range map[string]string{
		"redis_url":     `["app","env","SENTRY_DSN","generator"]`,
		"smtp_url":      `["app","env","SENTRY_DSN","generator"]`,
		"preview_url":   "",
		"random_secret": "",
		"postgres_url":  "",
	} {
		raw := strings.Replace(base, `"SENTRY_DSN":{"kind":"disabled"}`, `"SENTRY_DSN":{"kind":"generated","generator":`+jsonText(generator)+`}`, 1)
		_, err := ParsePreviewBlueprintContentV1([]byte(raw))
		if path == "" {
			if err != nil {
				t.Errorf("%s: %v", generator, err)
			}
			continue
		}
		if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{path}) {
			t.Errorf("%s: paths = %v", generator, got)
		}
	}
	serviceEnv := strings.Replace(base, `"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}`, `"POSTGRES_PASSWORD":{"kind":"generated","generator":"redis_url"}`, 1)
	if got := previewBlueprintIssuePaths(t, serviceEnv); !reflect.DeepEqual(got, []string{`["services","0","env","POSTGRES_PASSWORD","generator"]`}) {
		t.Fatalf("service env paths = %v", got)
	}
}

func TestPreviewBlueprintV1SkipsCrossFieldChecksAfterANonContinuableIssueLikeZod(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	raw := strings.Replace(base, `"services":[{"name":"db","role":"database"`, `"services":[{"name":"db","role":"queue"`, 1)
	if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{`["services","0","role"]`}) {
		t.Fatalf("paths = %v", got)
	}
	raw = strings.Replace(base, `"port":3000`, `"port":80`, 1)
	raw = strings.Replace(raw, `"services":[{"name":"db","role":"database","image":"docker.io/library/postgres@sha256:`+strings.Repeat("a", 64)+`","env":{"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}}}]`, `"services":[]`, 1)
	if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{`["app","env","DATABASE_URL","generator"]`, `["app","port"]`}) {
		t.Fatalf("paths = %v", got)
	}
}

func TestPreviewBlueprintV1OversizedArraysReportOnlyTheSizeIssueLikeZod(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	install := `["sk_live_x","","a","a","a","a","a","a","a"]`
	raw := strings.Replace(base, `"install":["pnpm install --frozen-lockfile"]`, `"install":`+install, 1)
	raw = strings.Replace(raw, `"port":3000`, `"port":80`, 1)
	if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{`["app","install"]`, `["app","port"]`}) {
		t.Fatalf("paths = %v", got)
	}
	hosts := make([]string, 33)
	for index := range hosts {
		hosts[index] = "LOCALHOST"
	}
	encoded, _ := json.Marshal(hosts)
	raw = strings.Replace(base, `"egress":{"mode":"none"}`, `"egress":{"mode":"allow","hosts":`+string(encoded)+`}`, 1)
	raw = strings.Replace(raw, `"name":"db"`, `"name":"DB"`, 1)
	if got := previewBlueprintIssuePaths(t, raw); !reflect.DeepEqual(got, []string{`["egress","hosts"]`, `["services","0","name"]`}) {
		t.Fatalf("paths = %v", got)
	}
}

func TestPreviewBlueprintV1LimitsRanges(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for limits, ok := range map[string]bool{
		`"memory_mb":128,"cpus":0.25,"max_lifetime_minutes":5`:     true,
		`"memory_mb":8192,"cpus":4,"max_lifetime_minutes":43200`:   true,
		`"memory_mb":127,"cpus":1,"max_lifetime_minutes":1440`:     false,
		`"memory_mb":8193,"cpus":1,"max_lifetime_minutes":1440`:    false,
		`"memory_mb":1024,"cpus":0,"max_lifetime_minutes":1440`:    false,
		`"memory_mb":1024,"cpus":2.75,"max_lifetime_minutes":1440`: true,
		`"memory_mb":1024,"cpus":1,"max_lifetime_minutes":43201`:   false,
		`"memory_mb":1024,"cpus":1,"max_lifetime_minutes":4.5e1`:   true,
	} {
		raw := strings.Replace(base, `"memory_mb":1024,"cpus":1,"max_lifetime_minutes":1440`, limits, 1)
		_, err := ParsePreviewBlueprintContentV1([]byte(raw))
		if (err == nil) != ok {
			t.Errorf("%s: err = %v, want ok %v", limits, err, ok)
		}
	}
}

func TestPreviewBlueprintV1EgressHostsAreUniqueValidDNSNames(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	egress := func(hosts ...string) string {
		encoded, _ := json.Marshal(hosts)
		return strings.Replace(base, `"egress":{"mode":"none"}`, `"egress":{"mode":"allow","hosts":`+string(encoded)+`}`, 1)
	}
	if _, err := ParsePreviewBlueprintContentV1([]byte(egress("api.stripe.com", "registry.npmjs.org"))); err != nil {
		t.Fatal(err)
	}
	if got := previewBlueprintIssuePaths(t, egress("a.example.com", "b.example.com", "a.example.com", "a.example.com")); !reflect.DeepEqual(got, []string{`["egress","hosts","2"]`, `["egress","hosts","3"]`}) {
		t.Fatalf("paths = %v", got)
	}
	long := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 61)
	if _, err := ParsePreviewBlueprintContentV1([]byte(egress(long))); err != nil {
		t.Fatalf("253 characters: %v", err)
	}
	if _, err := ParsePreviewBlueprintContentV1([]byte(egress(long + "a"))); err == nil {
		t.Fatal("254 characters accepted")
	}
	for _, host := range []string{
		"A.example.com", "example.com.", "-a.example.com", "a.1com", "a_b.example.com",
		"localhost", "api", "10.0.0.1", "api.localhost", "printer.local", "db.internal", "nas.lan", "router.home.arpa",
		"redis.default.svc", "git.corp", "a.test", "a.invalid", "a.onion", "a.example", "10.0.0.1.nip.io", "a.sslip.io",
		"localtest.me", "app.localtest.me", "lvh.me", "app.lvh.me", "a.localdomain", "a.intranet",
	} {
		_, err := ParsePreviewBlueprintContentV1([]byte(egress(host)))
		var contentErr *v1.PreviewBlueprintContentErrorV1
		if !errors.As(err, &contentErr) || len(contentErr.Issues) != 1 || !reflect.DeepEqual(contentErr.Issues[0].Path, []string{"egress", "hosts", "0"}) ||
			contentErr.Issues[0].Message != "Egress host must be a public DNS name, not localhost, an IP address or an internal suffix" {
			t.Errorf("host %q: err = %v", host, err)
		}
	}
	for _, host := range []string{"svc.io", "notlvh.me", "corp.example.com", "test.dev", "a.example.org"} {
		if _, err := ParsePreviewBlueprintContentV1([]byte(egress(host))); err != nil {
			t.Errorf("host %q: %v", host, err)
		}
	}
	hosts := make([]string, 33)
	for index := range hosts {
		hosts[index] = fmt.Sprintf("h%d.example.com", index)
	}
	if got := previewBlueprintIssuePaths(t, egress(hosts...)); !reflect.DeepEqual(got, []string{`["egress","hosts"]`}) {
		t.Fatalf("33 hosts: paths = %v", got)
	}
}

func TestPreviewBlueprintV1WorkingDirectoryRules(t *testing.T) {
	base := previewBlueprintBaseJSON(t)
	for directory, ok := range map[string]bool{
		".":          true,
		"apps/web":   true,
		"apps/..web": true,
		"apps\\web":  false,
		"..":         false,
		"":           false,
		"/":          false,
	} {
		raw := strings.Replace(base, `"working_directory":"."`, `"working_directory":`+jsonText(directory), 1)
		_, err := ParsePreviewBlueprintContentV1([]byte(raw))
		if (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok %v", directory, err, ok)
		}
	}
}
