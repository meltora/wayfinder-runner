package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func useNPMReportingVersion(t *testing.T, version string) fakeNPM {
	t.Helper()
	dir := t.TempDir()
	npm := fakeNPM{dir: dir, log: filepath.Join(dir, "calls.log")}
	script := "#!/bin/sh\n" +
		"printf '%s|%s|%s\\n' \"$(pwd -P)\" \"$*\" \"${npm_config_cache:-}\" >> '" + npm.log + "'\n" +
		"if [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\n" +
		fakeNPMInstallBody + "\n"
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return npm
}

func TestAnNPMBelowTheTestedMinimumStopsTheInstall(t *testing.T) {
	for _, version := range []string{"10.9.3", "10.8.99", "9.9.9", "8.19.4", "10.9.4-rc.1", "0.0.0"} {
		t.Run(version, func(t *testing.T) {
			f := newNPMResultTest(t)
			npm := useNPMReportingVersion(t, version)
			marker := filepath.Join(t.TempDir(), "ran")

			result := f.runCheck(t, npmInstallCheck("touch '"+marker+"'"))

			want := "dependency install for check check-0 needs npm 10.9.4 or later; the check environment has npm " + version
			if result["result"] != "error" || result["error_classification"] != "internal" || result["output"] != want {
				t.Fatalf("result = %v\nwant output %q", result, want)
			}
			if calls := npm.ciCalls(t); len(calls) != 0 {
				t.Fatalf("npm ci ran with npm %s: %v", version, calls)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the check command ran")
			}
		})
	}
}

func TestSupportedNPMVersionsInstall(t *testing.T) {
	for _, version := range []string{"10.9.4", "10.10.0", "11.0.0", "12.0.2", "12.0.2-pre.1", "100.0.0", "10.9.4+build.7"} {
		t.Run(version, func(t *testing.T) {
			f := newNPMResultTest(t)
			npm := useNPMReportingVersion(t, version)

			assertCheckResult(t, f.runCheck(t, npmInstallCheck("test -f node_modules/fixture-dep/index.js")), "passed")
			if got := len(npm.ciCalls(t)); got != 1 {
				t.Fatalf("npm ci ran %d times", got)
			}
		})
	}
}

func TestANonSemanticNPMVersionIsUnusable(t *testing.T) {
	for _, version := range []string{"v10.9.4", "10.9", "10.9.4.1", "ten", "010.9.4", "10.09.4", "10.9.4-", "99999999999999999999.0.0"} {
		t.Run(version, func(t *testing.T) {
			f := newNPMResultTest(t)
			npm := useNPMReportingVersion(t, version)

			result := f.runCheck(t, npmInstallCheck("true"))

			if result["output"] != "dependency install for check check-0 failed: npm reported an unusable version" {
				t.Fatalf("result = %v", result)
			}
			if calls := npm.ciCalls(t); len(calls) != 0 {
				t.Fatalf("npm ci ran: %v", calls)
			}
		})
	}
}
