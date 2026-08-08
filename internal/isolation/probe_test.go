package isolation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if err := ProbeNestedSandbox(context.Background(), t.TempDir(), "registry.example.test/job@sha256:abc"); err != nil {
		t.Fatalf("ProbeNestedSandbox: %v", err)
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
	err := ProbeNestedSandbox(context.Background(), t.TempDir(), "registry.example.test/job@sha256:abc")
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
	if err := ProbeNestedSandbox(context.Background(), file, "registry.example.test/job@sha256:abc"); err == nil {
		t.Fatal("file state root unexpectedly accepted")
	}
}
