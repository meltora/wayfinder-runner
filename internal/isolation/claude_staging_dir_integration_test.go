package isolation_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/isolation/claudeoffline"
)

const stagingDirSampler = `sample() { while :; do printf '%s ' "$(date +%s.%N)"; stat -c '%u:%g %a %n' "$PWD/.claude" "$PWD/.claude/.cc-writes" 2>&1 | tr '\n' ' '; echo; sleep 0.2; done; }
sample > /tmp/staging-samples.log 2>&1 &
sampler=$!
`

const stagingDirReport = `kill "$sampler" 2>/dev/null
echo "==staging-first"
grep -m1 ' 700 ' /tmp/staging-samples.log
echo "==staging-last"
tail -n 1 /tmp/staging-samples.log
echo "==staging-end"
`

func TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, providerCLIIntegrationRunner)
	if err != nil {
		t.Fatal(err)
	}
	if !mapping.RequiresOwnershipHandOff() {
		t.Fatalf("mapping %+v is not rootless; set DOCKER_HOST to the rootless daemon", mapping)
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(state, "jobs", "job-stagingprobe")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownership := isolation.DockerOwnership{Mapping: mapping, Image: image, StateDir: state}
	request := isolation.OwnershipRequest{
		RunnerID: providerCLIIntegrationRunner, AttemptID: "stagingprobe", Purpose: "job", Workspace: []string{workspace},
	}
	if err := ownership.HandOff(ctx, request); err != nil {
		t.Fatal(err)
	}
	reclaimed := false
	t.Cleanup(func() {
		if !reclaimed {
			_ = ownership.Reclaim(context.Background(), request)
		}
	})
	output := runOfflineClaudeInWorkspace(t, ctx, image, mapping, workspace)
	first, last := stagingSamples(output)
	t.Logf("first in-run sample: %s", first)
	t.Logf("last in-run sample: %s", last)
	if !strings.Contains(first, "10001:10001 700 "+workspace+"/.claude/.cc-writes") {
		t.Fatalf("Claude did not create a 0700 staging directory as the job user during the run: %s", output)
	}
	if !strings.Contains(last, "10001:10001 700 "+workspace+"/.claude/.cc-writes") {
		t.Fatalf("the staging directory did not stay for the whole run: %s", output)
	}
	staging := filepath.Join(workspace, ".claude", ".cc-writes")
	info, err := os.Lstat(staging)
	if err != nil {
		t.Fatalf("the staging directory did not survive the provider exit: %v", err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	t.Logf("host view after exit: uid %d gid %d mode %o", stat.Uid, stat.Gid, info.Mode().Perm())
	if int(stat.Uid) == os.Geteuid() || info.Mode().Perm() != 0o700 || !info.IsDir() {
		t.Fatalf("host view of the staging directory = uid %d mode %o", stat.Uid, info.Mode().Perm())
	}
	parent, err := os.OpenRoot(filepath.Join(workspace, ".claude"))
	if err != nil {
		t.Fatalf("the runner cannot open .claude: %v", err)
	}
	defer func() { _ = parent.Close() }()
	if _, err := parent.Lstat(".cc-writes"); err != nil {
		t.Fatalf("Lstat of the staging directory failed: %v", err)
	}
	_, openErr := parent.OpenRoot(".cc-writes")
	t.Logf("host OpenRoot of .claude/.cc-writes during hand-off: %v", openErr)
	if !errors.Is(openErr, fs.ErrPermission) {
		t.Fatalf("OpenRoot of the staging directory = %v, want permission denied", openErr)
	}
	if err := ownership.Reclaim(ctx, request); err != nil {
		t.Fatal(err)
	}
	reclaimed = true
	sub, err := parent.OpenRoot(".cc-writes")
	if err != nil {
		t.Fatalf("OpenRoot after reclaim failed: %v", err)
	}
	_ = sub.Close()
	assertNoProbeContainersRemain(t)
}

func runOfflineClaudeInWorkspace(t *testing.T, ctx context.Context, image string, mapping isolation.UserMapping, workspace string) string {
	t.Helper()
	probeDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(probeDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProbeFile(t, filepath.Join(probeDir, "messages-api.js"), claudeoffline.MessagesAPIScript(), 0o644)
	writeProbeFile(t, filepath.Join(probeDir, "bin", "bwrap"), loggingBubblewrap, 0o755)
	if err := os.Chmod(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	args, specFlags, stdin := offlineClaudeBuildSpec(t, "run the sandbox probe")
	script := strings.Replace(offlineClaudeBashScript, "cd /home/runner/work\n", "cd "+workspace+"\n"+stagingDirSampler, 1)
	script = strings.Replace(script, `echo "==end"`, stagingDirReport+`echo "==end"`, 1)
	script = strings.Replace(script, "export FAKE_BASH_COMMAND='", "export FAKE_BASH_COMMAND='sleep 2; ", 1)
	uid, gid, _ := strings.Cut(mapping.User(), ":")
	dockerArgs := []string{"run", "--rm", "-i", "--init", "--user", mapping.User(),
		"--cap-drop", "ALL", "--cap-add", "SYS_ADMIN", "--cap-add", "SYS_CHROOT",
		"--cap-add", "SETUID", "--cap-add", "SETGID", "--security-opt", "seccomp=unconfined",
		"--label", "com.ourplatform.runner-id=" + providerCLIIntegrationRunner,
		"--network", "none", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--tmpfs", "/home/runner:rw,nosuid,nodev,size=64m,uid=" + uid + ",gid=" + gid,
		"-e", "HOME=/home/runner",
		"-e", "PATH=" + isolation.UnprivilegedBubblewrapDir + ":" + isolation.JobImagePath,
		"-v", probeDir + ":/probe:ro",
		"-v", workspace + ":" + workspace + ":rw", "-w", workspace,
	}
	dockerArgs = append(dockerArgs, specFlags...)
	dockerArgs = append(dockerArgs, image, "sh", "-c", script, "offline-claude-staging")
	dockerArgs = append(dockerArgs, args...)
	command := exec.CommandContext(ctx, "docker", dockerArgs...)
	command.Stdin = bytes.NewReader(stdin)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("offline claude staging probe failed: %v: %s", err, output)
	}
	return string(output)
}

func stagingSamples(output string) (string, string) {
	section := ""
	var first, last string
	for _, line := range strings.Split(output, "\n") {
		switch line {
		case "==staging-first", "==staging-last", "==staging-end":
			section = line
			continue
		}
		switch section {
		case "==staging-first":
			first += line
		case "==staging-last":
			last += line
		}
	}
	return first, last
}
