package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/isolation"
)

func TestDockerDaemonRefusalNamesTheUnsupportedMode(t *testing.T) {
	for name, test := range map[string]struct {
		info isolation.DockerInfo
		goos string
		want string
	}{
		"unavailable":          {isolation.DockerInfo{}, "linux", "requires an available rootless Docker daemon"},
		"rootful linux":        {isolation.DockerInfo{Available: true}, "linux", "requires an available rootless Docker daemon"},
		"userns-remap":         {isolation.DockerInfo{Available: true, UsernsRemap: true}, "linux", "userns-remap Docker daemons are not supported yet"},
		"rootless":             {isolation.DockerInfo{Available: true, Rootless: true}, "linux", ""},
		"docker desktop macOS": {isolation.DockerInfo{Available: true}, "darwin", ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := dockerDaemonRefusal(test.info, test.goos)
			if test.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveDockerJobUserBuildsOwnershipOnlyForRootless(t *testing.T) {
	stateDir := t.TempDir()
	cfg := enrolledRestrictedDockerConfig(t)
	cfg.StateDir = stateDir
	var gotImage, gotRunner string
	resolveDockerUserMapping = func(_ context.Context, image, runnerID string) (isolation.UserMapping, error) {
		gotImage, gotRunner = image, runnerID
		return testRootlessMapping, nil
	}
	t.Cleanup(func() { resolveDockerUserMapping = isolation.ResolveUserMapping })
	mapping, ownership, err := resolveDockerJobUser(cfg, "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	docker, ok := ownership.(isolation.DockerOwnership)
	if mapping != testRootlessMapping || !ok || docker.Mapping != testRootlessMapping || docker.Image != cfg.JobImage || docker.StateDir != stateDir {
		t.Fatalf("mapping=%+v ownership=%+v", mapping, ownership)
	}
	if gotImage != cfg.JobImage || gotRunner != "runner-1" {
		t.Fatalf("resolved with image %q runner %q", gotImage, gotRunner)
	}
	resolveDockerUserMapping = func(context.Context, string, string) (isolation.UserMapping, error) {
		return testIdentityMapping, nil
	}
	mapping, ownership, err = resolveDockerJobUser(cfg, "runner-1")
	if err != nil || mapping != testIdentityMapping || ownership != nil {
		t.Fatalf("identity mapping=%+v ownership=%+v err=%v", mapping, ownership, err)
	}
}

func TestResolveDockerJobUserRefusesWithTheReason(t *testing.T) {
	cfg := enrolledRestrictedDockerConfig(t)
	for _, reason := range []error{isolation.ErrHostRoot, errors.New("the daemon's subordinate id range does not map the job user 10001")} {
		resolveDockerUserMapping = func(context.Context, string, string) (isolation.UserMapping, error) {
			return isolation.UserMapping{}, reason
		}
		_, _, err := resolveDockerJobUser(cfg, "runner-1")
		if err == nil || !strings.Contains(err.Error(), reason.Error()) {
			t.Fatalf("err = %v, want reason %q", err, reason)
		}
	}
	resolveDockerUserMapping = isolation.ResolveUserMapping
}

func installMappingDocker(t *testing.T, mapOutput string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
set -eu
echo "$1" >> '` + logPath + `'
case "$1" in
  info) echo '{"ServerVersion":"test","Driver":"overlay2","SecurityOptions":["name=rootless"]}' ;;
  network) echo '` + restrictedNetworkJSON("bridge", "local", "true", "local-dev-egress-not-enforced") + `' ;;
  ps) exit 0 ;;
  rm) exit 0 ;;
  run) printf '%b' "` + mapOutput + `" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return logPath
}

func rootlessMapOutput() string {
	return `         0 ` + strconv.Itoa(os.Getuid()) + `          1\n         1     100000      65536\n--\n         0 ` + strconv.Itoa(os.Getgid()) + `          1\n         1     100000      65536\n--\n10001\n10001\n`
}

func TestNewResolvesTheRootlessJobUserAfterContainerCleanupAndReclaimsLeftovers(t *testing.T) {
	logPath := installMappingDocker(t, rootlessMapOutput())
	cfg := enrolledRestrictedDockerConfig(t)
	leftover := filepath.Join(cfg.StateDir, "check-homes", "home-1")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if d.userMapping.Mode != isolation.UserMappingRootless || d.userMapping.User() != "10001:10001" || d.ownership == nil {
		t.Fatalf("mapping=%+v ownership=%v", d.userMapping, d.ownership)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(strings.Fields(string(calls)), " "); got != "info network ps run run" {
		t.Fatalf("docker calls = %q", got)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("leftover check home survived start: %v", err)
	}
}

func TestNewRefusesAnUnsupportedJobUserMapping(t *testing.T) {
	installMappingDocker(t, `         0     100000      65536\n--\n         0     100000      65536\n--\n10001\n10001\n`)
	_, err := New(enrolledRestrictedDockerConfig(t), nil)
	if err == nil || !strings.Contains(err.Error(), "userns-remap daemons are not supported yet") {
		t.Fatalf("err = %v", err)
	}
}
