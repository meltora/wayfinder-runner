package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerNameIsOpaqueAndStable(t *testing.T) {
	first := ContainerName("runner-1", "attempt-1")
	second := ContainerName("runner-1", "attempt-1")
	if first != second || len(first) != len("our-runner-")+24 {
		t.Fatalf("container name = %q, second = %q", first, second)
	}
	if first == ContainerName("runner-1", "attempt-2") {
		t.Fatal("different attempts received the same container name")
	}
}

func installFakeDocker(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestContainerCleanupValidatesDockerOutput(t *testing.T) {
	installFakeDocker(t, `
if [ "$1" = ps ]; then
  echo 0123456789abcdef0123456789abcdef
  exit 0
fi
if [ "$1" = rm ]; then exit 0; fi
exit 1`)
	if err := RemoveRunnerContainers(context.Background(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveJobContainer(context.Background(), "runner-1", "attempt-1"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRunnerContainers(context.Background(), ""); err == nil {
		t.Fatal("empty runner ID accepted")
	}

	installFakeDocker(t, `if [ "$1" = ps ]; then echo not-a-container-id; exit 0; fi; exit 0`)
	if err := RemoveRunnerContainers(context.Background(), "runner-1"); err == nil {
		t.Fatal("invalid Docker container ID accepted")
	}
}

func TestRestrictedNetworkLabels(t *testing.T) {
	installFakeDocker(t, `echo '{"com.ourplatform.egress-enforced":"true","com.ourplatform.egress-policy":"policy-v1"}'`)
	if err := VerifyDockerNetwork(context.Background(), "runner-egress", "policy-v1"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDockerNetwork(context.Background(), "", "policy-v1"); err == nil {
		t.Fatal("empty network accepted")
	}
	if err := VerifyDockerNetwork(context.Background(), "runner-egress", "other"); err == nil {
		t.Fatal("mismatched policy accepted")
	}
	installFakeDocker(t, `echo not-json`)
	if err := VerifyDockerNetwork(context.Background(), "runner-egress", "policy-v1"); err == nil {
		t.Fatal("malformed Docker labels accepted")
	}
}

func TestDockerEnvironmentUsesAllowlist(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/docker.sock")
	t.Setenv("RUNNER_PRIVATE_SECRET", "do-not-forward")
	env := strings.Join(DockerEnvironment(), "\n")
	if !strings.Contains(env, "DOCKER_HOST=unix:///tmp/docker.sock") {
		t.Fatalf("Docker host missing from %q", env)
	}
	if strings.Contains(env, "RUNNER_PRIVATE_SECRET") {
		t.Fatalf("secret leaked into Docker environment: %q", env)
	}
}
