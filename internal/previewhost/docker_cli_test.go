package previewhost

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
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
	cli := DockerCLI{TempDir: privateDir(t)}
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
	cli := DockerCLI{TempDir: privateDir(t)}
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
	cli := DockerCLI{TempDir: privateDir(t)}
	if _, err := cli.List(context.Background(), PreviewLabels(testRunnerID, testPreviewID)); err == nil {
		t.Fatal("invalid docker output accepted")
	}
}

func TestDockerCLIEnvFileIsPrivate(t *testing.T) {
	log := installFakeDocker(t, `
prev=
for arg do
  case "$prev" in --env-file) stat -c %a "$arg" > "$(dirname "$0")/env.mode"; stat -c %a "$(dirname "$arg")" > "$(dirname "$0")/dir.mode" ;; esac
  prev=$arg
done`)
	parent := t.TempDir()
	cli := DockerCLI{TempDir: filepath.Join(parent, "env")}
	if err := cli.RunDetached(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"env.mode": "600", "dir.mode": "700"} {
		mode, err := os.ReadFile(filepath.Join(filepath.Dir(log), name))
		if err != nil || strings.TrimSpace(string(mode)) != want {
			t.Fatalf("%s = %q %v, want %s", name, mode, err, want)
		}
	}
	if err := (DockerCLI{}).RunDetached(context.Background(), testSpec()); err == nil {
		t.Fatal("empty TempDir accepted")
	}
	if _, err := (DockerCLI{}).RunOnce(context.Background(), ContainerSpec{Name: "n", Image: "i", Network: "net"}); err == nil {
		t.Fatal("empty TempDir accepted for a container without environment")
	}
	shared := filepath.Join(parent, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := (DockerCLI{TempDir: shared}).RunDetached(context.Background(), testSpec()); err == nil {
		t.Fatal("group-accessible TempDir accepted")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(cli.TempDir, link); err != nil {
		t.Fatal(err)
	}
	if err := (DockerCLI{TempDir: link}).RunDetached(context.Background(), testSpec()); err == nil {
		t.Fatal("symlinked TempDir accepted")
	}
	if err := (DockerCLI{TempDir: "relative"}).RunDetached(context.Background(), testSpec()); err == nil {
		t.Fatal("relative TempDir accepted")
	}
	parentLink := filepath.Join(parent, "parent-link")
	if err := os.Symlink(parent, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := (DockerCLI{TempDir: filepath.Join(parentLink, "env")}).RunDetached(context.Background(), testSpec()); err == nil {
		t.Fatal("TempDir under a symlinked parent accepted")
	}
}

func TestOwnedByCurrentUserRefusesAnotherOwner(t *testing.T) {
	info, err := os.Lstat(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if !ownedByCurrentUser(info) {
		t.Fatal("runner-owned directory refused")
	}
	if os.Geteuid() != 0 {
		root, err := os.Lstat("/")
		if err != nil {
			t.Fatal(err)
		}
		if ownedByCurrentUser(root) {
			t.Fatal("root-owned directory accepted for a non-root runner")
		}
	}
}

func TestDockerCLIRemoveToleratesRemovalAlreadyInProgress(t *testing.T) {
	installFakeDocker(t, `
if [ "$1" = rm ]; then echo "Error response from daemon: removal of container 0123456789abcdef is already in progress" >&2; exit 1; fi`)
	if err := (DockerCLI{TempDir: privateDir(t)}).Remove(context.Background(), Objects{Containers: []string{"0123456789abcdef"}}); err != nil {
		t.Fatalf("removal already in progress was an error: %v", err)
	}
	installFakeDocker(t, `
if [ "$1" = rm ]; then echo "Error response from daemon: permission denied" >&2; exit 1; fi`)
	if err := (DockerCLI{TempDir: privateDir(t)}).Remove(context.Background(), Objects{Containers: []string{"0123456789abcdef"}}); err == nil {
		t.Fatal("a real removal error was tolerated")
	}
}

func TestDockerCLICancelSendsSIGTERM(t *testing.T) {
	log := installFakeDocker(t, `
marker="$(dirname "$0")/term"
sleep 30 >/dev/null 2>&1 &
child=$!
trap 'echo term > "$marker"; kill "$child"; exit 143' TERM
wait "$child"`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := (DockerCLI{TempDir: privateDir(t)}).RunOnce(ctx, ContainerSpec{Name: "n", Image: "i", Network: "net"}); err == nil {
		t.Fatal("cancelled run reported success")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("cancelled run took %s", elapsed)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(log), "term")); err != nil {
		t.Fatalf("docker run did not receive SIGTERM: %v", err)
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

func TestDockerCLIGatewayVerbs(t *testing.T) {
	log := installFakeDocker(t, `
case "$1" in
version) echo 27.3.1 ;;
import) cat > "$(dirname "$0")/stdin" ;;
esac`)
	cli := DockerCLI{TempDir: privateDir(t)}
	network := NetworkSpec{
		Name: "our-preview-0123456789ab", Labels: PreviewLabels(testRunnerID, testPreviewID), Internal: true,
		Options: map[string]string{"com.docker.network.bridge.inhibit_ipv4": "true"},
	}
	if err := cli.CreateNetwork(context.Background(), network); err != nil {
		t.Fatal(err)
	}
	if err := cli.ConnectNetwork(context.Background(), "our-preview-0123456789ab", "our-preview-0123456789ab-gateway", "our-gateway"); err != nil {
		t.Fatal(err)
	}
	version, err := cli.ServerVersion(context.Background())
	if err != nil || version != "27.3.1" {
		t.Fatalf("version = %q err = %v", version, err)
	}
	changes := []string{`ENTRYPOINT ["/our-preview-gateway","preview-gateway"]`, "LABEL com.ourplatform.preview-gateway=1"}
	if err := cli.ImportImage(context.Background(), "our-preview-gateway:0123456789abcdef", strings.NewReader("tar bytes"), changes); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, log)
	want := []string{
		"network create --driver bridge --internal -o com.docker.network.bridge.inhibit_ipv4=true " +
			"--label com.ourplatform.preview-host=runner-1 --label com.ourplatform.preview-id=preview-1 our-preview-0123456789ab",
		"network connect --alias our-gateway our-preview-0123456789ab our-preview-0123456789ab-gateway",
		"version --format {{.Server.Version}}",
		`import --change ENTRYPOINT ["/our-preview-gateway","preview-gateway"] --change LABEL com.ourplatform.preview-gateway=1 - our-preview-gateway:0123456789abcdef`,
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %q", calls)
	}
	stdin, err := os.ReadFile(filepath.Join(filepath.Dir(log), "stdin"))
	if err != nil || string(stdin) != "tar bytes" {
		t.Fatalf("import stdin = %q err = %v", stdin, err)
	}
	installFakeDocker(t, `echo "Cannot connect to the Docker daemon" >&2; exit 1`)
	if _, err := cli.ServerVersion(context.Background()); err == nil {
		t.Fatal("unreachable docker daemon reported a version")
	}
}
