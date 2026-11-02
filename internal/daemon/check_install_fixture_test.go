package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

const (
	fixturePackageJSON = `{"name":"fixture","version":"1.0.0"}` + "\n"
	fixtureLockfile    = `{"name":"fixture","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"fixture","version":"1.0.0"}}}` + "\n"
	fakeNPMVersion     = "10.0.0-fake"
	fakeNPMInstallBody = "mkdir -p node_modules/fixture-dep && printf 'module.exports = 1\\n' > node_modules/fixture-dep/index.js"
)

type fakeNPM struct {
	dir string
	log string
}

type npmCall struct {
	dir   string
	args  string
	cache string
}

func useFakeNPM(t *testing.T, ciBody string) fakeNPM {
	t.Helper()
	dir := t.TempDir()
	npm := fakeNPM{dir: dir, log: filepath.Join(dir, "calls.log")}
	script := "#!/bin/sh\n" +
		"printf '%s|%s|%s\\n' \"$(pwd -P)\" \"$*\" \"${npm_config_cache:-}\" >> '" + npm.log + "'\n" +
		"test -d \"${npm_config_cache:-/nonexistent}\" || { echo 'npm cache missing' >&2; exit 9; }\n" +
		"if [ \"$1\" = --version ]; then echo " + fakeNPMVersion + "; exit 0; fi\n" +
		ciBody + "\n"
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return npm
}

func (n fakeNPM) calls(t *testing.T) []npmCall {
	t.Helper()
	raw, err := os.ReadFile(n.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []npmCall
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		calls = append(calls, npmCall{dir: parts[0], args: parts[1], cache: parts[2]})
	}
	return calls
}

func (n fakeNPM) ciCalls(t *testing.T) []npmCall {
	t.Helper()
	var out []npmCall
	for _, call := range n.calls(t) {
		if strings.HasPrefix(call.args, "ci") {
			out = append(out, call)
		}
	}
	return out
}

func seedNPMProject(t *testing.T, dir string, directories ...string) {
	t.Helper()
	if len(directories) == 0 {
		directories = []string{"."}
	}
	for _, directory := range directories {
		writeAgentFile(t, dir, filepath.Join(directory, "package.json"), fixturePackageJSON)
		writeAgentFile(t, dir, filepath.Join(directory, "package-lock.json"), fixtureLockfile)
	}
}

func newNPMResultTest(t *testing.T, directories ...string) agentResultTest {
	t.Helper()
	return newAgentResultTestWith(t, func(origin string) { seedNPMProject(t, origin, directories...) })
}

func npmInstallCheck(script string, directories ...string) config.CheckConfig {
	check := shellCheck(script)
	check.Install = &config.CheckInstallConfig{Ecosystem: config.CheckInstallEcosystemNPM}
	if len(directories) != 0 {
		check.Install.Directories = directories
	}
	return check
}
