package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

const (
	goPassingTest = "package pass\n\nimport \"testing\"\n\nfunc TestPass(t *testing.T) {}\n"
	goDepTest     = "package pass\n\nimport (\n\t\"testing\"\n\n\t\"example.invalid/dep\"\n)\n\nfunc TestDep(t *testing.T) {\n\tif dep.Value != 1 {\n\t\tt.Fatal(dep.Value)\n\t}\n}\n"
	goDepSource   = "package dep\n\nconst Value = 1\n"
	goDepModule   = "module example.invalid/dep\n\ngo 1.21\n"
	goVendorList  = "# example.invalid/dep v1.0.0\n## explicit; go 1.21\nexample.invalid/dep\n"
	goRequireDep  = "module example.test/pass\n\ngo 1.21\n\nrequire example.invalid/dep v1.0.0\n"
)

func useHermeticGoHome(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	home := t.TempDir()
	env := "GOPROXY=off\nGOFLAGS=\nGOSUMDB=off\nGOTOOLCHAIN=local\n"
	if err := os.MkdirAll(filepath.Join(home, ".config", "go"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "go", "env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
}

func goTestCheck() config.CheckConfig {
	return config.CheckConfig{Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 300}
}

func TestGoChecksUseOnlyThePublishedTree(t *testing.T) {
	for _, tc := range []struct {
		name  string
		seed  func(t *testing.T, origin string)
		agent func(t *testing.T, checkout string)
		want  string
		cause string
	}{
		{"a standard-library module passes", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "go.mod", "module example.test/pass\n\ngo 1.21\n")
			writeAgentFile(t, origin, "pass_test.go", goPassingTest)
		}, nil, "passed", ""},
		{"a replace into ignored node_modules fails", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, ".gitignore", "node_modules/\n")
			writeAgentFile(t, origin, "go.mod", goRequireDep+"\nreplace example.invalid/dep => ./node_modules/dep\n")
			writeAgentFile(t, origin, "dep_test.go", goDepTest)
		}, func(t *testing.T, checkout string) {
			writeAgentFile(t, checkout, "node_modules/dep/go.mod", goDepModule)
			writeAgentFile(t, checkout, "node_modules/dep/dep.go", goDepSource)
		}, "failed", "replacement directory ./node_modules/dep does not exist"},
		{"a workspace that uses an ignored module fails", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, ".gitignore", "tools/\n")
			writeAgentFile(t, origin, "go.mod", "module example.test/pass\n\ngo 1.21\n")
			writeAgentFile(t, origin, "go.work", "go 1.21\n\nuse (\n\t.\n\t./tools\n)\n")
			writeAgentFile(t, origin, "pass_test.go", goPassingTest)
		}, func(t *testing.T, checkout string) {
			writeAgentFile(t, checkout, "tools/go.mod", "module example.test/tools\n\ngo 1.21\n")
			writeAgentFile(t, checkout, "tools/tools.go", "package tools\n")
		}, "failed", "cannot load module tools listed in go.work file"},
		{"a dependency vendored only in the agent's ignored vendor fails", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, ".gitignore", "vendor/\n")
			writeAgentFile(t, origin, "go.mod", goRequireDep)
			writeAgentFile(t, origin, "dep_test.go", goDepTest)
		}, func(t *testing.T, checkout string) {
			writeAgentFile(t, checkout, "vendor/modules.txt", goVendorList)
			writeAgentFile(t, checkout, "vendor/example.invalid/dep/dep.go", goDepSource)
		}, "failed", "example.invalid/dep"},
		{"a published vendor passes", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "go.mod", goRequireDep)
			writeAgentFile(t, origin, "dep_test.go", goDepTest)
			writeAgentFile(t, origin, "vendor/modules.txt", goVendorList)
			writeAgentFile(t, origin, "vendor/example.invalid/dep/dep.go", goDepSource)
		}, nil, "passed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useHermeticGoHome(t)
			f := newAgentResultTestWith(t, func(origin string) { tc.seed(t, origin) })
			if tc.agent != nil {
				tc.agent(t, f.checkout)
			}

			result := f.runCheck(t, goTestCheck())

			output, _ := result["output"].(string)
			if result["result"] != tc.want || !strings.Contains(output, tc.cause) {
				t.Fatalf("result = %v, want %s caused by %q", result, tc.want, tc.cause)
			}
		})
	}
}
