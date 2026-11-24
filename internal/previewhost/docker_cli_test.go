package previewhost

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func installFakeDocker(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	path := filepath.Join(dir, "docker")
	body := "#!/bin/sh\nset -eu\necho \"$*\" >> " + log + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return log
}

func readCalls(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func testSpec() ContainerSpec {
	return ContainerSpec{
		Name: "our-preview-0123456789ab-app", Image: testImage("node"), Network: "our-preview-0123456789ab",
		NetworkAlias: "app", Labels: PreviewLabels(testRunnerID, testPreviewID),
		Env:  map[string]string{"PATH": "/override", "SECRET": "s3cr3t-value-123"},
		User: "10001:10001", CapAdd: []string{"SETUID"}, ReadOnly: true,
		Tmpfs:   []string{"/tmp:rw,nosuid,nodev,size=256m"},
		Mounts:  []Mount{{Source: "/state/previews/x/workspace", Target: "/workspace"}},
		WorkDir: "/workspace/web", MemoryMB: 512, CPUs: 1.5, PidsLimit: 512, PublishPort: 3000,
		Entrypoint: "/bin/sh", Command: []string{"-c", "npm start"},
	}
}

func TestRunArgsEnforceHardening(t *testing.T) {
	args := runArgs(testSpec(), "/state/env-file", true)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run -d --name our-preview-0123456789ab-app",
		"--cap-drop ALL", "--cap-add SETUID", "--security-opt no-new-privileges", "--pids-limit 512",
		"--memory 512m", "--memory-swap 512m", "--cpus 1.5", "--network our-preview-0123456789ab", "--network-alias app",
		"--label com.ourplatform.preview-host=runner-1", "--label com.ourplatform.preview-id=preview-1",
		"--user 10001:10001", "--read-only", "--tmpfs /tmp:rw,nosuid,nodev,size=256m",
		"--mount type=bind,source=/state/previews/x/workspace,target=/workspace",
		"-w /workspace/web", "-p 127.0.0.1::3000", "--env-file /state/env-file", "--entrypoint /bin/sh",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	tail := args[len(args)-3:]
	if !slices.Equal(tail, []string{testImage("node"), "-c", "npm start"}) {
		t.Fatalf("args tail = %v", tail)
	}
	for _, forbidden := range []string{"--privileged", "--network host", "--pid host", "--ipc host", "docker.sock", "runner-id", "attempt-id", "s3cr3t"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("args %q contain %q", joined, forbidden)
		}
	}
	once := strings.Join(runArgs(ContainerSpec{Name: "n", Image: "i", Network: "net", Labels: PreviewLabels("r", "p")}, "", false), " ")
	if !strings.HasPrefix(once, "run --rm --name n") || strings.Contains(once, " -d ") || strings.Contains(once, "--env-file") {
		t.Fatalf("one-shot args = %q", once)
	}
	if !strings.Contains(once, "--cap-drop ALL") || !strings.Contains(once, "--security-opt no-new-privileges") {
		t.Fatalf("one-shot args lack hardening: %q", once)
	}
}

func TestDockerCLIPassesEnvironmentThroughFileOnly(t *testing.T) {
	log := installFakeDocker(t, `
prev=
for arg do
  case "$prev" in --env-file) cp "$arg" "$(dirname "$0")/env.copy" ;; esac
  prev=$arg
done
echo started`)
	t.Setenv("RUNNER_PRIVATE_SECRET", "do-not-forward")
	cli := DockerCLI{TempDir: t.TempDir()}
	if err := cli.RunDetached(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, log)
	if len(calls) != 1 || strings.Contains(calls[0], "s3cr3t") {
		t.Fatalf("calls = %v", calls)
	}
	envCopy, err := os.ReadFile(filepath.Join(filepath.Dir(log), "env.copy"))
	if err != nil {
		t.Fatal(err)
	}
	if string(envCopy) != "PATH=/override\nSECRET=s3cr3t-value-123\n" {
		t.Fatalf("env file = %q", envCopy)
	}
	entries, err := os.ReadDir(cli.TempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("env file was not removed: %v %v", entries, err)
	}
}

func TestDockerCLIListAndRemoveUseBothPreviewLabels(t *testing.T) {
	log := installFakeDocker(t, `
case "$1 $2" in
  "ps -aq") echo 0123456789abcdef ;;
  "network ls") echo our-preview-0123456789ab ;;
  "volume ls") echo ;;
esac`)
	cli := DockerCLI{TempDir: t.TempDir()}
	objects, err := cli.List(context.Background(), PreviewLabels(testRunnerID, testPreviewID))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(objects.Containers, []string{"0123456789abcdef"}) || !slices.Equal(objects.Networks, []string{"our-preview-0123456789ab"}) ||
		len(objects.Volumes) != 0 {
		t.Fatalf("objects = %+v", objects)
	}
	if err := cli.Remove(context.Background(), objects); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, log)
	filters := "--filter label=com.ourplatform.preview-host=runner-1 --filter label=com.ourplatform.preview-id=preview-1"
	for _, prefix := range []string{"ps -aq --no-trunc ", "network ls -q --no-trunc ", "volume ls -q "} {
		found := false
		for _, call := range calls {
			if strings.HasPrefix(call, prefix) && strings.Contains(call, filters) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no %q call with %q in %v", prefix, filters, calls)
		}
	}
	if !slices.Contains(calls, "rm -f -v 0123456789abcdef") || !slices.Contains(calls, "network rm our-preview-0123456789ab") {
		t.Fatalf("remove calls = %v", calls)
	}
	if _, err := cli.List(context.Background(), map[string]string{LabelPreviewHost: testRunnerID}); err == nil {
		t.Fatal("list without a preview id label was accepted")
	}
}

func TestDockerCLIRejectsInvalidListOutput(t *testing.T) {
	installFakeDocker(t, `echo 'bad id; rm -rf /'`)
	cli := DockerCLI{TempDir: t.TempDir()}
	if _, err := cli.List(context.Background(), PreviewLabels(testRunnerID, testPreviewID)); err == nil {
		t.Fatal("invalid docker output accepted")
	}
}

func TestDockerCLIHostPort(t *testing.T) {
	installFakeDocker(t, `echo 127.0.0.1:49153`)
	port, err := DockerCLI{}.HostPort(context.Background(), "our-preview-0123456789ab-app", 3000)
	if err != nil || port != 49153 {
		t.Fatalf("port = %d err = %v", port, err)
	}
	installFakeDocker(t, `echo 0.0.0.0:49153`)
	if _, err := (DockerCLI{}).HostPort(context.Background(), "our-preview-0123456789ab-app", 3000); err == nil {
		t.Fatal("port published on all interfaces accepted")
	}
}
