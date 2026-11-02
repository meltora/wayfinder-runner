package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example/app-runner/internal/config"
)

func TestInstalledLinksInsideTheCheckWorkspaceAreKept(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) {
		seedNPMProject(t, origin)
		writeAgentFile(t, origin, "packages/local/index.js", "module.exports = 'local'\n")
	})
	useFakeNPM(t, "mkdir -p node_modules/pkg/bin node_modules/.bin && echo run > node_modules/pkg/bin/run.js && "+
		"ln -s ../pkg/bin/run.js node_modules/.bin/run && ln -s ../packages/local node_modules/local && "+
		"ln -s ../../other-repo/lib node_modules/sibling")

	result := f.runCheck(t, npmInstallCheck("test -L node_modules/.bin/run && test -L node_modules/local && test -f node_modules/local/index.js && test -L node_modules/sibling"))

	assertCheckResult(t, result, "passed")
}

func TestInstalledLinksThatLeaveTheCheckWorkspaceAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name, link, target, want string
	}{
		{"absolute", "node_modules/abs", "/etc", "node_modules/abs"},
		{"relative escape", "node_modules/up", "../../../..", "node_modules/up"},
		{"nested escape", "node_modules/pkg/deep", "../../../../../outside", "node_modules/pkg/deep"},
		{"through a sibling", "node_modules/sibling", "../../other/../../../outside", "node_modules/sibling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNPMResultTest(t)
			useFakeNPM(t, "mkdir -p node_modules/pkg && ln -s '"+tc.target+"' '"+tc.link+"'")
			marker := filepath.Join(t.TempDir(), "ran")

			result := f.runCheck(t, npmInstallCheck("touch '"+marker+"'"))

			want := "dependency install for check check-0 produced a symbolic link that leaves the check workspace: " + tc.want
			if result["result"] != "error" || result["error_classification"] != "internal" || result["output"] != want {
				t.Fatalf("result = %v\nwant %q", result, want)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("check ran after a scan refusal")
			}
		})
	}
}

func TestInstalledSpecialFilesAreRefusedWithTheirRepositoryPath(t *testing.T) {
	f := newNPMResultTest(t, "web")
	useFakeNPM(t, "mkdir -p node_modules/pkg && mkfifo node_modules/pkg/pipe")

	result := f.runCheck(t, npmInstallCheck("true", "web"))

	if result["output"] != "dependency install for check check-0 produced a special file: web/node_modules/pkg/pipe" {
		t.Fatalf("result = %v", result)
	}
}

func TestInstalledDependencyLimitsCountAcrossAllDirectories(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit func(*checkWorkspace)
		want  string
	}{
		{"entries", func(w *checkWorkspace) { w.entryLimit = 3 }, "installed dependencies have more than 3 entries"},
		{"bytes", func(w *checkWorkspace) { w.byteLimit = 12 }, "installed dependencies have more than 12 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNPMResultTest(t, "a", "b")
			useFakeNPM(t, "mkdir -p node_modules/pkg && echo content > node_modules/pkg/index.js")
			tc.limit(f.jr.checkWorkspace())

			result := f.runCheck(t, npmInstallCheck("true", "a", "b"))

			if result["result"] != "error" || result["output"] != tc.want {
				t.Fatalf("result = %v, want %q", result, tc.want)
			}
		})
	}
	t.Run("within the limits", func(t *testing.T) {
		f := newNPMResultTest(t, "a", "b")
		useFakeNPM(t, "mkdir -p node_modules/pkg && echo content > node_modules/pkg/index.js")
		f.jr.checkWorkspace().entryLimit, f.jr.checkWorkspace().byteLimit, f.jr.checkWorkspace().depthLimit = 4, 16, 2

		assertCheckResult(t, f.runCheck(t, npmInstallCheck("true", "a", "b")), "passed")
	})
}

func TestTheInstalledTreeDigestCoversPathKindModeSizeAndContent(t *testing.T) {
	base := "mkdir -p node_modules/pkg && printf 'a' > node_modules/pkg/index.js && ln -s pkg/index.js node_modules/link"
	digests := map[string]string{}
	for _, tc := range []struct{ name, body string }{
		{"base", base},
		{"same", base},
		{"content", "mkdir -p node_modules/pkg && printf 'b' > node_modules/pkg/index.js && ln -s pkg/index.js node_modules/link"},
		{"size", "mkdir -p node_modules/pkg && printf 'aa' > node_modules/pkg/index.js && ln -s pkg/index.js node_modules/link"},
		{"mode", base + " && chmod +x node_modules/pkg/index.js"},
		{"path", "mkdir -p node_modules/pkh && printf 'a' > node_modules/pkh/index.js && ln -s pkh/index.js node_modules/link"},
		{"link target", "mkdir -p node_modules/pkg && printf 'a' > node_modules/pkg/index.js && ln -s pkg/./index.js node_modules/link"},
		{"kind", "mkdir -p node_modules/pkg node_modules/link && printf 'a' > node_modules/pkg/index.js"},
	} {
		f := newNPMResultTest(t)
		useFakeNPM(t, tc.body)
		commit := f.publish(t)
		f.jr.job.RequiredChecks = []string{"lint"}
		f.jr.d.cfg.Checks = map[string]config.CheckConfig{"lint": npmInstallCheck("true")}
		results, err := f.jr.checkResults(context.Background(), nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		report, err := f.jr.runCheckRequest(context.Background(), checkRequest{checkID: "lint", check: f.jr.d.cfg.Checks["lint"], repository: f.repository()}, results)
		if err != nil || report.install == nil || report.install.Directories[0].TreeDigest == nil || report.commit != commit {
			t.Fatalf("%s: report = %+v, %v", tc.name, report, err)
		}
		digests[tc.name] = *report.install.Directories[0].TreeDigest
	}
	if digests["base"] != digests["same"] {
		t.Fatal("the digest is not deterministic")
	}
	seen := map[string]string{}
	for name, digest := range digests {
		if name == "same" {
			continue
		}
		if other, ok := seen[digest]; ok {
			t.Fatalf("%s and %s share a digest", name, other)
		}
		seen[digest] = name
	}
}

func TestLeavesCheckRootIsLexical(t *testing.T) {
	for _, tc := range []struct {
		link, target string
		leaves       bool
	}{
		{"repositories/r/node_modules/.bin/x", "../pkg/x.js", false},
		{"repositories/r/node_modules/a", "../../b/lib", false},
		{"repositories/r/node_modules/a", "../../../../x", true},
		{"repositories/r/node_modules/a", "/abs", true},
		{"repositories/r/node_modules/a", "", true},
		{"a", "..", true},
		{"a/b", "../..", true},
	} {
		if got := leavesCheckRoot(tc.link, tc.target); got != tc.leaves {
			t.Fatalf("leavesCheckRoot(%q, %q) = %v", tc.link, tc.target, got)
		}
	}
}
