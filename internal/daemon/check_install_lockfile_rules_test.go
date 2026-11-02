package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testIntegrity  = `"sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="`
	testHTTPSTgz   = `"https://registry.npmjs.org/x/-/x-1.0.0.tgz"`
	testRootPrefix = `{"lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.0.0"}`
)

func lockfileEntry(key, body string) string {
	return testRootPrefix + `,"` + key + `":{` + body + `}}}`
}

func TestLockfileRulesAcceptTheJobImageNPMLockfile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "job-image", "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bundled := strings.Count(string(raw), `"inBundle": true`); bundled < 100 {
		t.Fatalf("the job image lockfile has %d bundled entries; this test needs a lockfile with bundled packages", bundled)
	}
	if problem, key := inspectNPMLockfile(raw); problem != lockfileValid {
		t.Fatalf("problem = %v key = %q", problem, key)
	}
}

func TestLockfileRulesRefuseMalformedLockfiles(t *testing.T) {
	for name, content := range map[string]string{
		"version 2 with only dependencies": `{"lockfileVersion":2,"requires":true,"dependencies":{"evil":{"version":"1.0.0","resolved":"http://127.0.0.1:18765/evil-1.0.0.tgz"}}}`,
		"version 3 with packages array":    `{"lockfileVersion":3,"packages":[]}`,
		"version 3 with null packages":     `{"lockfileVersion":3,"packages":null}`,
		"version 2 with empty packages":    `{"lockfileVersion":2,"packages":{}}`,
		"packages without a root entry":    `{"lockfileVersion":3,"packages":{"node_modules/x":{"resolved":` + testHTTPSTgz + `,"integrity":` + testIntegrity + `}}}`,
		"root entry that is not an object": `{"lockfileVersion":3,"packages":{"":[]}}`,
		"entry that is not an object":      testRootPrefix + `,"node_modules/x":1}}`,
		"duplicate integrity":              lockfileEntry("node_modules/x", `"resolved":`+testHTTPSTgz+`,"integrity":`+testIntegrity+`,"integrity":""`),
		"duplicate package key":            testRootPrefix + `,"node_modules/x":{"integrity":` + testIntegrity + `},"node_modules/x":{}}}`,
		"duplicate packages":               `{"lockfileVersion":3,"packages":{"":{}},"packages":{}}`,
		"duplicate lockfileVersion":        `{"lockfileVersion":3,"lockfileVersion":1,"packages":{"":{}}}`,
		"numeric integrity":                lockfileEntry("node_modules/x", `"integrity":1`),
		"string link":                      lockfileEntry("node_modules/x", `"link":"true"`),
		"string inBundle":                  lockfileEntry("node_modules/x", `"inBundle":"true"`),
		"numeric resolved":                 lockfileEntry("node_modules/x", `"resolved":1,"integrity":`+testIntegrity),
		"null link":                        lockfileEntry("node_modules/x", `"link":null`),
		"trailing data":                    testRootPrefix + `}} {}`,
		"version 3 as a string":            `{"lockfileVersion":"3","packages":{"":{}}}`,
		"version 1":                        `{"lockfileVersion":1,"packages":{"":{}}}`,
		"missing version":                  `{"packages":{"":{}}}`,
		"not an object":                    `[3]`,
	} {
		t.Run(name, func(t *testing.T) {
			if problem, key := inspectNPMLockfile([]byte(content)); problem != lockfileMalformed {
				t.Fatalf("problem = %v key = %q, want malformed", problem, key)
			}
		})
	}
}

func TestLockfileRulesRequireIntegrityForInstalledPackages(t *testing.T) {
	for name, entry := range map[string]string{
		"capitalised Integrity": `"resolved":"http://127.0.0.1:18765/evil-1.0.0.tgz","Integrity":` + testIntegrity,
		"upper-case INTEGRITY":  `"resolved":` + testHTTPSTgz + `,"INTEGRITY":` + testIntegrity,
		"capitalised Link":      `"resolved":` + testHTTPSTgz + `,"Link":true`,
		"capitalised InBundle":  `"resolved":` + testHTTPSTgz + `,"InBundle":true`,
		"capitalised Resolved":  `"Resolved":"file:vendor/x.tgz"`,
		"no resolved":           `"version":"1.0.0"`,
		"git protocol":          `"resolved":"git://example.test/x.git#abc"`,
		"github shorthand":      `"resolved":"github:o/r"`,
		"ssh":                   `"resolved":"ssh://git@example.test/x.git"`,
		"upper-case FILE":       `"resolved":"FILE:vendor/x.tgz"`,
		"blank integrity":       `"resolved":"http://mirror.example/x.tgz","integrity":" "`,
		"malformed integrity":   `"resolved":` + testHTTPSTgz + `,"integrity":"garbage"`,
		"unknown algorithm":     `"resolved":` + testHTTPSTgz + `,"integrity":"md5-AAAA"`,
		"link false":            `"resolved":` + testHTTPSTgz + `,"link":false`,
		"inBundle false":        `"version":"1.0.0","inBundle":false`,
	} {
		t.Run(name, func(t *testing.T) {
			problem, key := inspectNPMLockfile([]byte(lockfileEntry("node_modules/x", entry)))
			if problem != lockfileMissingIntegrity || key != "node_modules/x" {
				t.Fatalf("problem = %v key = %q, want missing integrity for node_modules/x", problem, key)
			}
		})
	}
}

func TestLockfileRulesReportTheFirstNestedPackageWithoutIntegrity(t *testing.T) {
	content := testRootPrefix +
		`,"packages/app":{"name":"app","version":"1.0.0"}` +
		`,"packages/app/node_modules/zeta":{"version":"1.0.0"}` +
		`,"node_modules/beta":{"version":"1.0.0"}` +
		`,"node_modules/alpha":{"resolved":` + testHTTPSTgz + `,"integrity":` + testIntegrity + `}}}`
	if problem, key := inspectNPMLockfile([]byte(content)); problem != lockfileMissingIntegrity || key != "node_modules/beta" {
		t.Fatalf("problem = %v key = %q", problem, key)
	}
	nested := testRootPrefix + `,"packages/app/node_modules/zeta":{"version":"1.0.0"}}}`
	if problem, key := inspectNPMLockfile([]byte(nested)); problem != lockfileMissingIntegrity || key != "packages/app/node_modules/zeta" {
		t.Fatalf("nested problem = %v key = %q", problem, key)
	}
}

func TestLockfileRulesAcceptExemptEntries(t *testing.T) {
	content := `{"name":"fixture","lockfileVersion":2,"requires":true,"packages":{"":{"name":"fixture","workspaces":["packages/*"]}` +
		`,"packages/app":{"name":"app","version":"1.0.0"}` +
		`,"node_modules/app":{"resolved":"packages/app","link":true}` +
		`,"node_modules/local":{"resolved":"file:vendor/local-1.0.0.tgz"}` +
		`,"node_modules/fromgit":{"resolved":"git+https://example.test/x.git#0123456789abcdef0123456789abcdef01234567"}` +
		`,"node_modules/registry":{"version":"1.0.0","resolved":` + testHTTPSTgz + `,"integrity":` + testIntegrity + `}` +
		`,"node_modules/legacy":{"version":"1.0.0","integrity":"sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA= sha512-AAAA"}` +
		`},"dependencies":{"anything":{"version":"1.0.0"}}}`
	if problem, key := inspectNPMLockfile([]byte(content)); problem != lockfileValid {
		t.Fatalf("problem = %v key = %q", problem, key)
	}
}

func bundlerLockfile(bundler, entries string) string {
	return testRootPrefix + `,"node_modules/a":{"version":"1.0.0","resolved":` + testHTTPSTgz + `,"integrity":` + testIntegrity + bundler + `}` + entries + `}}`
}

func TestLockfileRulesExemptOnlyPackagesTheirBundlerShips(t *testing.T) {
	for name, content := range map[string]string{
		"a declared bundled dependency": bundlerLockfile(`,"bundleDependencies":["inner"]`,
			`,"node_modules/a/node_modules/inner":{"version":"1.0.0","inBundle":true}`),
		"a scoped bundled dependency": bundlerLockfile(`,"bundleDependencies":["@s/inner"]`,
			`,"node_modules/a/node_modules/@s/inner":{"version":"1.0.0","inBundle":true}`),
		"a dependency of a bundled dependency": bundlerLockfile(`,"bundleDependencies":["inner"]`,
			`,"node_modules/a/node_modules/inner":{"version":"1.0.0","inBundle":true,"dependencies":{"helper":"1.0.0"}}`+
				`,"node_modules/a/node_modules/helper":{"version":"1.0.0","inBundle":true,"optionalDependencies":{"deep":"1.0.0"}}`+
				`,"node_modules/a/node_modules/helper/node_modules/deep":{"version":"1.0.0","inBundle":true}`),
		"a child of a bundled dependency": bundlerLockfile(`,"bundleDependencies":["inner"]`,
			`,"node_modules/a/node_modules/inner":{"version":"1.0.0","inBundle":true}`+
				`,"node_modules/a/node_modules/inner/node_modules/child":{"version":"1.0.0","inBundle":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			if problem, key := inspectNPMLockfile([]byte(content)); problem != lockfileValid {
				t.Fatalf("problem = %v key = %q", problem, key)
			}
		})
	}
}

func TestLockfileRulesRefuseBundlesNoPackageShips(t *testing.T) {
	for name, tc := range map[string]struct{ content, key string }{
		"a top-level bundled package":          {lockfileEntry("node_modules/evil", `"version":"1.0.0","inBundle":true`), "node_modules/evil"},
		"a top-level package the root bundles": {`{"lockfileVersion":3,"packages":{"":{"name":"fixture","bundleDependencies":["evil"]},"node_modules/evil":{"version":"1.0.0","inBundle":true}}}`, "node_modules/evil"},
		"an undeclared bundled package": {bundlerLockfile(``,
			`,"node_modules/a/node_modules/evil":{"version":"1.0.0","inBundle":true}`), "node_modules/a/node_modules/evil"},
		"a bundled package declared under another name": {bundlerLockfile(`,"bundleDependencies":["other"]`,
			`,"node_modules/a/node_modules/evil":{"version":"1.0.0","inBundle":true}`), "node_modules/a/node_modules/evil"},
		"a bundle declared as true": {bundlerLockfile(`,"bundleDependencies":true,"dependencies":{"evil":"1.0.0"}`,
			`,"node_modules/a/node_modules/evil":{"version":"1.0.0","inBundle":true}`), "node_modules/a/node_modules/evil"},
		"a dependency of an unbundled package": {bundlerLockfile(`,"bundleDependencies":["inner"]`,
			`,"node_modules/a/node_modules/inner":{"version":"1.0.0","inBundle":true}`+
				`,"node_modules/plain":{"version":"1.0.0","resolved":`+testHTTPSTgz+`,"integrity":`+testIntegrity+`,"dependencies":{"evil":"1.0.0"}}`+
				`,"node_modules/a/node_modules/evil":{"version":"1.0.0","inBundle":true}`), "node_modules/a/node_modules/evil"},
		"a bundle under a linked package": {testRootPrefix + `,"node_modules/a":{"resolved":"packages/a","link":true,"bundleDependencies":["evil"]}` +
			`,"node_modules/a/node_modules/evil":{"version":"1.0.0","inBundle":true}}}`, "node_modules/a/node_modules/evil"},
		"a bundle under a workspace folder": {testRootPrefix + `,"packages/app":{"name":"app","bundleDependencies":["evil"]}` +
			`,"packages/app/node_modules/evil":{"version":"1.0.0","inBundle":true}}}`, "packages/app/node_modules/evil"},
		"mutually dependent bundled packages nobody declares": {bundlerLockfile(``,
			`,"node_modules/a/node_modules/x":{"version":"1.0.0","inBundle":true,"dependencies":{"y":"1.0.0"}}`+
				`,"node_modules/a/node_modules/y":{"version":"1.0.0","inBundle":true,"peerDependencies":{"x":"1.0.0"}}`), "node_modules/a/node_modules/x"},
		"a bundle whose parent entry is missing": {testRootPrefix + `,"node_modules/a/node_modules/evil":{"version":"1.0.0","inBundle":true}}}`, "node_modules/a/node_modules/evil"},
		"a git package without a commit":         {lockfileEntry("node_modules/x", `"resolved":"git+https://127.0.0.1:18765/x.git"`), "node_modules/x"},
		"a git package with a branch":            {lockfileEntry("node_modules/x", `"resolved":"git+https://127.0.0.1:18765/x.git#main"`), "node_modules/x"},
	} {
		t.Run(name, func(t *testing.T) {
			if problem, key := inspectNPMLockfile([]byte(tc.content)); problem != lockfileMissingIntegrity || key != tc.key {
				t.Fatalf("problem = %v key = %q, want missing integrity for %s", problem, key, tc.key)
			}
		})
	}
}

func TestLockfileRulesRefuseMistypedBundleFields(t *testing.T) {
	for name, content := range map[string]string{
		"string bundleDependencies":   bundlerLockfile(`,"bundleDependencies":"inner"`, ``),
		"numeric bundled name":        bundlerLockfile(`,"bundleDependencies":[1]`, ``),
		"array dependencies":          bundlerLockfile(`,"dependencies":["inner"]`, ``),
		"duplicate dependency names":  bundlerLockfile(`,"dependencies":{"inner":"1","inner":"2"}`, ``),
		"string optionalDependencies": bundlerLockfile(`,"optionalDependencies":"inner"`, ``),
		"null peerDependencies":       bundlerLockfile(`,"peerDependencies":null`, ``),
	} {
		t.Run(name, func(t *testing.T) {
			if problem, key := inspectNPMLockfile([]byte(content)); problem != lockfileMalformed {
				t.Fatalf("problem = %v key = %q, want malformed", problem, key)
			}
		})
	}
}
