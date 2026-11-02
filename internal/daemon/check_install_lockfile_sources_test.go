package daemon

import (
	"slices"
	"strings"
	"testing"
)

func TestNPMConfigOutsideTheInstallDirectoryAncestorsIsIgnored(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) { seedNPMProject(t, origin, "web/app") })
	for _, name := range []string{"docs/.npmrc", "web/other/.npmrc", "web/app/sub/.npmrc", "web/app/.npmrc.example", "web/app/.NPMRC", "web/app.npmrc"} {
		agentCommit(t, f.checkout, name, "registry=http://127.0.0.1:1/\n")
	}
	npm := useFakeNPM(t, fakeNPMInstallBody)

	assertCheckResult(t, f.runCheck(t, npmInstallCheck("test -f web/app/node_modules/fixture-dep/index.js", "web/app")), "passed")
	if got := len(npm.ciCalls(t)); got != 1 {
		t.Fatalf("npm ci ran %d times", got)
	}
}

func TestNPMConfigCandidatesCoverTheInstallDirectoryAndEveryAncestor(t *testing.T) {
	for directory, want := range map[string][]string{
		".":       {".npmrc"},
		"web":     {".npmrc", "web/.npmrc"},
		"a/b/c":   {".npmrc", "a/.npmrc", "a/b/.npmrc", "a/b/c/.npmrc"},
		"@s/pkg":  {".npmrc", "@s/.npmrc", "@s/pkg/.npmrc"},
		"web/app": {".npmrc", "web/.npmrc", "web/app/.npmrc"},
	} {
		if got := npmConfigCandidates(directory); !slices.Equal(got, want) {
			t.Fatalf("%s: candidates = %v, want %v", directory, got, want)
		}
	}
}

func TestLockfileRulesRefuseGitPackages(t *testing.T) {
	for name, resolved := range map[string]string{
		"git over https pinned to a commit": "git+https://example.test/x.git#0123456789abcdef0123456789abcdef01234567",
		"git over ssh pinned to a commit":   "git+ssh://git@example.test/x.git#" + strings.Repeat("ab", 32),
		"git over file pinned to a commit":  "git+file:///srv/x.git#0123456789abcdef0123456789abcdef01234567",
	} {
		t.Run(name, func(t *testing.T) {
			problem, key := inspectNPMLockfile([]byte(lockfileEntry("node_modules/g", `"version":"1.0.0","resolved":"`+resolved+`"`)), false)
			if problem != lockfileMissingIntegrity || key != "node_modules/g" {
				t.Fatalf("problem = %v key = %q, want missing integrity for node_modules/g", problem, key)
			}
		})
	}
}

func TestLockfileRulesRequireASHA512Integrity(t *testing.T) {
	for name, integrity := range map[string]string{
		"sha1 only":            "sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"sha256 only":          "sha256-" + strings.Repeat("A", 43) + "=",
		"sha384 only":          "sha384-" + strings.Repeat("A", 64),
		"short sha512":         "sha512-AAAA",
		"sha512 without pad":   "sha512-" + strings.Repeat("A", 86),
		"long sha512":          "sha512-" + strings.Repeat("A", 87) + "==",
		"sha512 with a suffix": testSHA512 + "?foo",
		"sha1 and a bad token": "sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA= sha512-AAAA",
		"sha512 and garbage":   testSHA512 + " garbage",
	} {
		t.Run(name, func(t *testing.T) {
			problem, key := inspectNPMLockfile([]byte(lockfileEntry("node_modules/x", `"resolved":`+testHTTPSTgz+`,"integrity":"`+integrity+`"`)), false)
			if problem != lockfileMissingIntegrity || key != "node_modules/x" {
				t.Fatalf("problem = %v key = %q, want missing integrity for node_modules/x", problem, key)
			}
		})
	}
	for name, integrity := range map[string]string{
		"sha512":                testSHA512,
		"sha512 and sha1":       testSHA512 + " sha1-AAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"tab separated sha512s": testSHA512 + `\t` + testSHA512,
	} {
		t.Run(name, func(t *testing.T) {
			if problem, key := inspectNPMLockfile([]byte(lockfileEntry("node_modules/x", `"resolved":`+testHTTPSTgz+`,"integrity":"`+integrity+`"`)), false); problem != lockfileValid {
				t.Fatalf("problem = %v key = %q", problem, key)
			}
		})
	}
}

func rootBundledLockfile(root, parent string) string {
	return `{"lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.0.0","dependencies":{"p":"1.0.0"}` + root + `}` +
		`,"node_modules/p":{"version":"1.0.0","resolved":` + testHTTPSTgz + `,"integrity":` + testIntegrity + `,"dependencies":{"evil":"1.0.0"},"bundleDependencies":["evil"]` + parent + `}` +
		`,"node_modules/p/node_modules/evil":{"version":"1.0.0","resolved":"http://127.0.0.1:18799/evil-1.0.0.tgz","inBundle":true}}}`
}

func TestLockfileRulesGrantNoBundleExemptionWhenTheRootBundles(t *testing.T) {
	if problem, key := inspectNPMLockfile([]byte(rootBundledLockfile(``, ``)), false); problem != lockfileValid {
		t.Fatalf("control: problem = %v key = %q", problem, key)
	}
	for name, tc := range map[string]struct {
		lockfile        string
		manifestBundles bool
	}{
		"the published package.json bundles":             {rootBundledLockfile(``, ``), true},
		"the lockfile root declares bundleDependencies":  {rootBundledLockfile(`,"bundleDependencies":["p"]`, ``), false},
		"the lockfile root declares bundledDependencies": {rootBundledLockfile(`,"bundledDependencies":["p"]`, ``), false},
		"the lockfile root declares a true bundle":       {rootBundledLockfile(`,"bundleDependencies":true`, ``), false},
		"the lockfile root declares an empty bundle":     {rootBundledLockfile(`,"bundleDependencies":[]`, ``), false},
		"a top-level package is marked inBundle":         {rootBundledLockfile(``, `,"inBundle":true`), false},
	} {
		t.Run(name, func(t *testing.T) {
			problem, key := inspectNPMLockfile([]byte(tc.lockfile), tc.manifestBundles)
			if problem != lockfileMissingIntegrity || key != "node_modules/p/node_modules/evil" {
				t.Fatalf("problem = %v key = %q, want missing integrity for node_modules/p/node_modules/evil", problem, key)
			}
		})
	}
}

func TestManifestBundlesReadsOnlyTheExactBundleKeys(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		bundles bool
		valid   bool
	}{
		"no bundle":                     {`{"name":"x","dependencies":{"p":"1.0.0"}}`, false, true},
		"bundleDependencies list":       {`{"bundleDependencies":["p"]}`, true, true},
		"bundledDependencies list":      {`{"bundledDependencies":["p"]}`, true, true},
		"bundleDependencies true":       {`{"bundleDependencies":true}`, true, true},
		"bundleDependencies false":      {`{"bundleDependencies":false}`, true, true},
		"bundleDependencies empty":      {`{"bundleDependencies":[]}`, true, true},
		"bundleDependencies null":       {`{"bundleDependencies":null}`, true, true},
		"escaped bundle key":            {"{\"bundle\\" + "u0044ependencies\":[\"p\"]}", true, true},
		"other case is not read by npm": {`{"BundleDependencies":["p"]}`, false, true},
		"duplicate bundle keys":         {`{"bundleDependencies":[],"bundleDependencies":["p"]}`, false, false},
		"duplicate unrelated keys":      {`{"name":"a","name":"b"}`, false, false},
		"not an object":                 {`["bundleDependencies"]`, false, false},
		"not JSON":                      {`{bundleDependencies`, false, false},
		"trailing data":                 {`{} {}`, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			bundles, ok := manifestBundles([]byte(tc.content))
			if bundles != tc.bundles || ok != tc.valid {
				t.Fatalf("bundles = %v ok = %v, want %v %v", bundles, ok, tc.bundles, tc.valid)
			}
		})
	}
}
