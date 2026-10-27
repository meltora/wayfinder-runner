package isolation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func fakeProbeJSON(json string) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) { return []byte(json), nil }
}

func TestProbeRootless(t *testing.T) {
	info := probeDocker(context.Background(), fakeProbeJSON(
		`{"SecurityOptions":["name=seccomp,profile=builtin","name=rootless","name=cgroupns"],"ServerVersion":"26.1.0","Driver":"overlay2"}`))
	if !info.Available || !info.Rootless {
		t.Fatalf("info = %+v", info)
	}
	if info.ServerVersion != "26.1.0" {
		t.Fatalf("version = %q", info.ServerVersion)
	}
}

func TestProbeRootful(t *testing.T) {
	info := probeDocker(context.Background(), fakeProbeJSON(
		`{"SecurityOptions":["name=seccomp,profile=builtin"],"ServerVersion":"26.1.0","Driver":"overlay2"}`))
	if !info.Available || info.Rootless {
		t.Fatalf("info = %+v", info)
	}
}

func TestProbeUserns(t *testing.T) {
	info := probeDocker(context.Background(), fakeProbeJSON(
		`{"SecurityOptions":["name=userns"],"ServerVersion":"26.1.0"}`))
	if !info.UsernsRemap {
		t.Fatalf("info = %+v", info)
	}
}

func TestProbeUnavailable(t *testing.T) {
	info := probeDocker(context.Background(), func(context.Context) ([]byte, error) {
		return nil, errors.New("docker not found")
	})
	if info.Available {
		t.Fatal("should be unavailable")
	}
}

func TestNestedSandboxProbeWithDockerBoundary(t *testing.T) {
	installFakeDocker(t, `
if [ "$1" = run ]; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -w ]; then
      shift
      printf passed > "$1/probe-output"
      exit 0
    fi
    shift
  done
  exit 3
fi
if [ "$1" = rm ]; then exit 0; fi
exit 4`)
	if err := ProbeNestedSandbox(context.Background(), t.TempDir(), "registry.example.test/job@sha256:abc", testIdentityMapping); err != nil {
		t.Fatalf("ProbeNestedSandbox: %v", err)
	}
}

var testIdentityMapping = UserMapping{Mode: UserMappingIdentity, ContainerUID: 1000, ContainerGID: 1000, HostUID: 1000, HostGID: 1000}

func installRootlessProbeDocker(t *testing.T, handOffExit, reclaimExit, removeExit int) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls")
	installFakeDocker(t, `
FAKE_DOCKER_LOG='`+log+`'
user=""
prev=""
for arg do
  if [ "$prev" = --user ]; then user="$arg"; fi
  prev="$arg"
done
case "$1" in
  run)
    if [ "$user" = 0:0 ]; then
      case "$*" in
        *" sh -c "*) echo "handoff" >> "$FAKE_DOCKER_LOG"; exit `+strconv.Itoa(handOffExit)+` ;;
        *" chown -hR 0:0 "*) echo "reclaim" >> "$FAKE_DOCKER_LOG"; exit `+strconv.Itoa(reclaimExit)+` ;;
      esac
      exit 7
    fi
    echo "probe $user" >> "$FAKE_DOCKER_LOG"
    while [ "$#" -gt 0 ]; do
      if [ "$1" = -w ]; then shift; printf passed > "$1/probe-output"; exit 0; fi
      shift
    done
    exit 3 ;;
  rm) echo "rm" >> "$FAKE_DOCKER_LOG"; exit `+strconv.Itoa(removeExit)+` ;;
esac
exit 4`)
	return log
}

func probeCalls(t *testing.T, log string) string {
	t.Helper()
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(string(calls), "\n", " ")), " ")
}

func probeTrees(t *testing.T, state string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "doctor"))
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestNestedSandboxProbeReclaimsWhatWasHandedOffWhenHandOffFails(t *testing.T) {
	log := installRootlessProbeDocker(t, 5, 0, 0)
	state := t.TempDir()
	err := ProbeNestedSandbox(context.Background(), state, "registry.example.test/job@sha256:abc", testRootlessMapping)
	if err == nil || !strings.Contains(err.Error(), "hand job trees to the container user") {
		t.Fatalf("hand-off failure err = %v", err)
	}
	if got := probeCalls(t, log); got != "handoff rm reclaim" {
		t.Fatalf("docker calls = %q", got)
	}
	if count := probeTrees(t, state); count != 0 {
		t.Fatalf("doctor has %d entries after a reclaimed hand-off failure", count)
	}
}

func TestNestedSandboxProbeNeverReclaimsOrRemovesWhileTheProbeContainerMayRun(t *testing.T) {
	log := installRootlessProbeDocker(t, 0, 0, 6)
	state := t.TempDir()
	if err := ProbeNestedSandbox(context.Background(), state, "registry.example.test/job@sha256:abc", testRootlessMapping); err == nil {
		t.Fatal("probe passed although its container could not be removed")
	}
	if got := probeCalls(t, log); got != "handoff probe 10001:10001 rm" {
		t.Fatalf("docker calls = %q", got)
	}
	if count := probeTrees(t, state); count != 1 {
		t.Fatalf("doctor has %d entries, want the unreclaimed probe tree kept", count)
	}
}

func TestNestedSandboxProbeFailsAndKeepsTheTreeWhenReclaimFails(t *testing.T) {
	log := installRootlessProbeDocker(t, 0, 8, 0)
	state := t.TempDir()
	err := ProbeNestedSandbox(context.Background(), state, "registry.example.test/job@sha256:abc", testRootlessMapping)
	if err == nil || !strings.Contains(err.Error(), "re-own job output for the runner") {
		t.Fatalf("reclaim failure err = %v", err)
	}
	if got := probeCalls(t, log); got != "handoff probe 10001:10001 rm reclaim rm" {
		t.Fatalf("docker calls = %q", got)
	}
	if count := probeTrees(t, state); count != 1 {
		t.Fatalf("doctor has %d entries, want the unreclaimed probe tree kept", count)
	}
}

func TestNestedSandboxProbeHandsOffAndReclaimsUnderRootless(t *testing.T) {
	log := installRootlessProbeDocker(t, 0, 0, 0)
	state := t.TempDir()
	if err := ProbeNestedSandbox(context.Background(), state, "registry.example.test/job@sha256:abc", testRootlessMapping); err != nil {
		t.Fatalf("ProbeNestedSandbox: %v", err)
	}
	if got := probeCalls(t, log); got != "handoff probe 10001:10001 rm reclaim" {
		t.Fatalf("docker calls = %q", got)
	}
	if count := probeTrees(t, state); count != 0 {
		t.Fatalf("doctor has %d entries after a passing probe", count)
	}
}

func TestNestedSandboxProbeRefusesAnUnresolvedMappingBeforeDocker(t *testing.T) {
	installFakeDocker(t, `exit 9`)
	state := t.TempDir()
	if err := ProbeNestedSandbox(context.Background(), state, "registry.example.test/job@sha256:abc", UserMapping{}); err == nil {
		t.Fatal("unresolved mapping accepted")
	}
	if _, err := os.Stat(filepath.Join(state, "doctor")); !os.IsNotExist(err) {
		t.Fatalf("probe tree created for a refused mapping: %v", err)
	}
}

func TestNestedSandboxProbeReportsBoundedFailure(t *testing.T) {
	installFakeDocker(t, `
if [ "$1" = run ]; then
  i=0
  while [ "$i" -lt 40000 ]; do printf x; i=$((i+1)); done
  exit 9
fi
if [ "$1" = rm ]; then exit 0; fi
exit 4`)
	err := ProbeNestedSandbox(context.Background(), t.TempDir(), "registry.example.test/job@sha256:abc", testIdentityMapping)
	if err == nil || len(err.Error()) > (34<<10) {
		t.Fatalf("expected bounded probe failure, got length=%d err=%v", len(err.Error()), err)
	}
}

func TestCappedProbeBuffer(t *testing.T) {
	var buffer cappedProbeBuffer
	payload := bytes.Repeat([]byte("x"), 64<<10)
	n, err := buffer.Write(payload)
	if err != nil || n != len(payload) || len(buffer.String()) != 32<<10 {
		t.Fatalf("Write=%d err=%v retained=%d", n, err, len(buffer.String()))
	}
}

func TestNestedSandboxProbeRejectsUnusableStateRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProbeNestedSandbox(context.Background(), file, "registry.example.test/job@sha256:abc", testIdentityMapping); err == nil {
		t.Fatal("file state root unexpectedly accepted")
	}
}
