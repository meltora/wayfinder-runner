package isolation

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/example/app-runner/internal/safeio"
)

func rootlessGate(t *testing.T) (string, UserMapping) {
	t.Helper()
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	image := os.Getenv("JOB_IMAGE")
	if image == "" {
		t.Fatal("JOB_IMAGE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mapping, err := ResolveUserMapping(ctx, image, "itest")
	if err != nil {
		t.Fatal(err)
	}
	if !mapping.RequiresOwnershipHandOff() {
		t.Fatalf("mapping %+v is not rootless; set DOCKER_HOST to the rootless daemon", mapping)
	}
	return image, mapping
}

func integrationState(t *testing.T) string {
	t.Helper()
	state, err := os.MkdirTemp("", "rdju-state-")
	if err != nil {
		t.Fatal(err)
	}
	state, err = filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeOwnedTree(state) })
	return state
}

func mkdirs(t *testing.T, mode os.FileMode, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func ownerAndMode(t *testing.T, path string) (uint32, uint32, os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return stat.Uid, stat.Gid, info.Mode().Perm()
}

func expectOwner(t *testing.T, path string, uid, gid uint32, mode os.FileMode) {
	t.Helper()
	gotUID, gotGID, gotMode := ownerAndMode(t, path)
	if gotUID != uid || gotGID != gid || gotMode != mode {
		t.Fatalf("%s is %d:%d %v, want %d:%d %v", path, gotUID, gotGID, gotMode, uid, gid, mode)
	}
}

func runWrapped(t *testing.T, policy Policy, script string) string {
	t.Helper()
	command, args, err := Docker{}.Wrap("sh", []string{"-c", script}, policy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	output, runErr := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err := RemoveJobContainer(context.Background(), policy.RunnerID, policy.AttemptID); err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("container failed: %v\n%s", runErr, output)
	}
	return string(output)
}

func TestOwnershipHandOffAndReclaimOnARootlessDaemon(t *testing.T) {
	image, mapping := rootlessGate(t)
	state := integrationState(t)
	runnerUID, runnerGID := uint32(os.Getuid()), uint32(os.Getgid())
	jobUID, jobGID := uint32(mapping.HostUID), uint32(mapping.HostGID)
	workspace := filepath.Join(state, "jobs", "job-1")
	home := filepath.Join(state, "job-homes", "home-1")
	socket := filepath.Join(state, "sockets", "job-1")
	pack := filepath.Join(workspace, "pack")
	mkdirs(t, 0o700, workspace, home, socket, filepath.Join(workspace, "sub"), filepath.Join(pack, "skills"))
	writeFile(t, filepath.Join(workspace, "runner.txt"), "runner\n", 0o600)
	writeFile(t, filepath.Join(home, "auth.json"), "{}\n", 0o600)
	writeFile(t, filepath.Join(socket, "job.token"), "token\n", 0o400)
	writeFile(t, filepath.Join(pack, "WORKFLOW.md"), "# workflow\n", 0o444)
	mkdirs(t, 0o555, filepath.Join(pack, "skills"))
	mkdirs(t, 0o555, pack)
	outsideTarget := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outsideTarget, "outside\n", 0o644)
	if err := os.Symlink("/etc/codex/requirements.toml", filepath.Join(workspace, "requirements-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideTarget, filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}
	ownership := DockerOwnership{Mapping: mapping, Image: image, StateDir: state}
	request := OwnershipRequest{
		RunnerID: "itest", AttemptID: "rdju-handoff", Purpose: "job",
		Workspace: []string{workspace}, Private: []string{home, socket},
	}
	ctx := context.Background()
	if err := ownership.HandOff(ctx, request); err != nil {
		t.Fatal(err)
	}
	expectOwner(t, workspace, jobUID, runnerGID, 0o750)
	expectOwner(t, filepath.Join(workspace, "runner.txt"), jobUID, runnerGID, 0o640)
	expectOwner(t, home, jobUID, jobGID, 0o700)
	expectOwner(t, socket, jobUID, jobGID, 0o700)
	if _, err := os.Lstat(filepath.Join(socket, "job.token")); err == nil {
		t.Fatal("the runner can still traverse the handed-off private socket directory")
	}
	expectOwner(t, filepath.Join(pack, "WORKFLOW.md"), jobUID, runnerGID, 0o444)
	expectOwner(t, filepath.Join(pack, "skills"), jobUID, runnerGID, 0o555)
	expectOwner(t, outsideTarget, runnerUID, runnerGID, 0o644)
	if uid, _, _ := ownerAndMode(t, filepath.Join(workspace, "outside-link")); uid != jobUID {
		t.Fatalf("symlink itself was not re-owned: %d", uid)
	}

	token := filepath.Join(socket, "job.token")
	policy := Policy{
		User: mapping.User(), NetworkNone: true, ReadOnlyFS: true, WorkDir: workspace, WorkspaceRoot: workspace,
		JobHome: home, MCPSock: filepath.Join(socket, "mcp.sock"), MCPTokenFile: token,
		Image: image, RunnerID: "itest", AttemptID: "rdju-hostile",
	}
	output := runWrapped(t, policy, strings.Join([]string{
		"set -eu",
		"cat runner.txt >/dev/null",
		"cat /home/runner/auth.json >/dev/null",
		`echo "home-file=$(stat -c '%u:%g %a' /home/runner/auth.json)"`,
		`echo "token=$(stat -c '%u:%g %a' "$PLATFORM_MCP_TOKEN_FILE")"`,
		`cat "$PLATFORM_MCP_TOKEN_FILE" >/dev/null`,
		"echo public > public.txt",
		"echo private > private.txt; chmod 600 private.txt",
		"mkdir -p hostile/inner",
		"echo secret > hostile/inner/secret; chmod 600 hostile/inner/secret",
		"mkfifo hostile/fifo",
		"chmod 000 hostile/inner hostile",
		"stat -c '%u:%g %a' /etc/codex/requirements.toml",
		"echo done",
	}, "\n"))
	if !strings.Contains(output, "0:0 644") || !strings.Contains(output, "done") ||
		!strings.Contains(output, "home-file=10001:10001 600") || !strings.Contains(output, "token=10001:10001 400") {
		t.Fatalf("job container output:\n%s", output)
	}
	for _, readable := range []string{"runner.txt", "public.txt"} {
		file, err := safeio.OpenRegular(workspace, readable)
		if err != nil {
			t.Fatalf("runner cannot read %s while job-owned: %v", readable, err)
		}
		_, _ = io.Copy(io.Discard, file)
		_ = file.Close()
	}
	if file, err := safeio.OpenRegular(workspace, "private.txt"); err == nil {
		_ = file.Close()
		t.Fatal("runner read a job-created 0600 file before reclaim")
	}
	if err := os.RemoveAll(workspace); err == nil {
		t.Fatal("job-owned tree was removable before reclaim")
	}

	if err := ownership.Reclaim(ctx, request); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{workspace, home, socket} {
		if err := safeio.MakeTreeRemovable(root); err != nil {
			t.Fatal(err)
		}
		if err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if uid, gid, _ := ownerAndMode(t, path); uid != runnerUID || gid != runnerGID {
				return fmt.Errorf("%s is still %d:%d after reclaim", path, uid, gid)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	expectOwner(t, filepath.Join(socket, "job.token"), runnerUID, runnerGID, 0o400)
	expectOwner(t, outsideTarget, runnerUID, runnerGID, 0o644)
	if info, err := os.Lstat(filepath.Join(workspace, "hostile", "fifo")); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("fifo after reclaim: %v %v", info, err)
	}
	for _, root := range []string{workspace, home, socket} {
		if err := os.RemoveAll(root); err != nil {
			t.Fatalf("remove %s after reclaim: %v", root, err)
		}
	}
	expectNoRunnerContainers(t, "itest")
}

func expectNoRunnerContainers(t *testing.T, runnerID string) {
	t.Helper()
	output, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.ourplatform.runner-id="+runnerID).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("containers left with the runner label: %s", output)
	}
}

func TestJobUserSecurityPropertiesInANestedContainer(t *testing.T) {
	image, mapping := rootlessGate(t)
	state := integrationState(t)
	workspace := filepath.Join(state, "jobs", "job-2")
	home := filepath.Join(state, "job-homes", "home-2")
	socketDir := filepath.Join(state, "sockets", "job-2")
	mkdirs(t, 0o700, workspace, home, socketDir)
	writeFile(t, filepath.Join(workspace, "probe-input"), "readable\n", 0o600)
	mkdirs(t, 0o700, filepath.Join(home, ".codex"))
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), "{\"test_only\":true}\n", 0o600)
	if err := writeCodexProfiles(home); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(socketDir, "job.token")
	writeFile(t, token, "token\n", 0o400)
	socketPath := filepath.Join(socketDir, "mcp.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("pong\n"))
			_ = conn.Close()
		}
	}()
	ownership := DockerOwnership{Mapping: mapping, Image: image, StateDir: state}
	request := OwnershipRequest{
		RunnerID: "itest", AttemptID: "rdju-nested", Purpose: "job",
		Workspace: []string{workspace}, Private: []string{home, socketDir},
	}
	ctx := context.Background()
	if err := ownership.HandOff(ctx, request); err != nil {
		t.Fatal(err)
	}
	reclaimed := false
	defer func() {
		if !reclaimed {
			_ = ownership.Reclaim(ctx, request)
		}
	}()
	nested := Policy{
		User: mapping.User(), NetworkNone: true, ReadOnlyFS: true, WorkDir: workspace, WorkspaceRoot: workspace,
		JobHome: home, MCPSock: socketPath, MCPTokenFile: token, Image: image,
		RunnerID: "itest", AttemptID: "rdju-nested-job", NestedSandbox: true,
	}
	output := runWrapped(t, nested, strings.Join([]string{
		`echo "uid=$(id -u)"`,
		`if mount -o remount,rw / 2>/dev/null; then echo remount=allowed; else echo remount=denied; fi`,
		`if touch /usr/rdju 2>/dev/null; then echo usr=allowed; else echo usr=denied; fi`,
		`if cat "$PLATFORM_MCP_TOKEN_FILE" >/dev/null; then echo token=read; fi`,
		`echo "socket=$(printf ping | socat - UNIX-CONNECT:$PLATFORM_MCP_SOCKET)"`,
		`codex sandbox -p runner-build -P runner-job -C "$PWD" -- sh -ceu 'test -r ./probe-input; printf passed > ./probe-output; if cat /home/runner/.codex/auth.json >/dev/null 2>&1; then exit 42; fi'`,
		`echo "codex=$?"`,
	}, "\n"))
	for _, want := range []string{"uid=10001", "remount=denied", "usr=denied", "token=read", "socket=pong", "codex=0"} {
		if !strings.Contains(output, want) {
			t.Fatalf("nested container output lacks %q:\n%s", want, output)
		}
	}
	plain := nested
	plain.NestedSandbox = false
	plain.AttemptID = "rdju-plain-job"
	plain.MCPSock, plain.MCPTokenFile = "", ""
	output = runWrapped(t, plain, `grep -E '^(NoNewPrivs|CapEff):' /proc/self/status`)
	if !strings.Contains(output, "NoNewPrivs:\t1") || !strings.Contains(output, "CapEff:\t0000000000000000") {
		t.Fatalf("non-nested container status:\n%s", output)
	}
	if err := ownership.Reclaim(ctx, request); err != nil {
		t.Fatal(err)
	}
	reclaimed = true
	if data, err := os.ReadFile(filepath.Join(workspace, "probe-output")); err != nil || string(data) != "passed" {
		t.Fatalf("probe output = %q %v", data, err)
	}
	expectNoRunnerContainers(t, "itest")
}

func TestOwnershipTimingOnALargeTree(t *testing.T) {
	image, mapping := rootlessGate(t)
	state := integrationState(t)
	workspace := filepath.Join(state, "jobs", "job-3")
	mkdirs(t, 0o700, workspace)
	for dir := 0; dir < 1000; dir++ {
		directory := filepath.Join(workspace, fmt.Sprintf("d%04d", dir))
		mkdirs(t, 0o755, directory)
		for file := 0; file < 99; file++ {
			writeFile(t, filepath.Join(directory, fmt.Sprintf("f%02d", file)), "x", 0o644)
		}
	}
	ownership := DockerOwnership{Mapping: mapping, Image: image, StateDir: state}
	request := OwnershipRequest{RunnerID: "itest", AttemptID: "rdju-timing", Purpose: "job", Workspace: []string{workspace}}
	start := time.Now()
	if err := ownership.HandOff(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	handOff := time.Since(start)
	start = time.Now()
	if err := ownership.Reclaim(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	t.Logf("100000-entry tree: hand-off %v, reclaim %v", handOff, time.Since(start))
}
