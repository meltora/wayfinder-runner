package isolation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func agentPackMountPolicy(jobRoot string, readOnlyWorkDir bool) Policy {
	return Policy{
		NetworkNone: true, ReadOnlyWorkDir: readOnlyWorkDir,
		WorkspaceRoot: jobRoot, WorkDir: filepath.Join(jobRoot, "repositories", "primary"),
		AgentPackDir: filepath.Join(jobRoot, "pack"),
		RunnerID:     "runner-1", AttemptID: "attempt-1",
	}
}

func mountIndex(t *testing.T, args []string, mount string) int {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-v" && args[index+1] == mount {
			return index
		}
	}
	t.Fatalf("docker args %q have no -v %s", strings.Join(args, " "), mount)
	return -1
}

func TestDockerWrapMountsTheAgentPackReadOnlyInsideTheJobRoot(t *testing.T) {
	d := Docker{LookPath: func(string) (string, error) { return "/usr/bin/docker", nil }}
	for _, tc := range []struct {
		name            string
		readOnlyWorkDir bool
		rootMode        string
	}{
		{name: "build", readOnlyWorkDir: false, rootMode: "rw"},
		{name: "plan", readOnlyWorkDir: true, rootMode: "ro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, args, err := d.Wrap("claude", []string{"-p", "hi"}, agentPackMountPolicy("/jobs/a1", tc.readOnlyWorkDir))
			if err != nil {
				t.Fatal(err)
			}
			root := mountIndex(t, args, "/jobs/a1:/jobs/a1:"+tc.rootMode)
			pack := mountIndex(t, args, "/jobs/a1/pack:/jobs/a1/pack:ro")
			if pack < root {
				t.Fatalf("pack mount precedes the job-root mount: %q", strings.Join(args, " "))
			}
			for index := 0; index+1 < len(args); index++ {
				if args[index] == "-v" && strings.HasPrefix(args[index+1], "/jobs/a1/pack:") && !strings.HasSuffix(args[index+1], ":ro") {
					t.Fatalf("pack has a writable mount: %s", args[index+1])
				}
			}
		})
	}
}

func TestAgentPackMountRefusesEveryWriteInsideTheContainer(t *testing.T) {
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	image := os.Getenv("JOB_IMAGE")
	if image == "" {
		t.Fatal("JOB_IMAGE is required")
	}
	jobRoot, err := os.MkdirTemp("", "ap8-pack-mount-")
	if err != nil {
		t.Fatal(err)
	}
	jobRoot, err = filepath.EvalSymlinks(jobRoot)
	if err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(jobRoot, "pack")
	checkout := filepath.Join(jobRoot, "repositories", "primary")
	t.Cleanup(func() { _ = os.RemoveAll(jobRoot) })
	for _, dir := range []string{filepath.Join(pack, "skills"), checkout} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pack, "WORKFLOW.md"), []byte("# workflow"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := agentPackMountPolicy(jobRoot, false)
	p.Image = image
	p.AttemptID = "ap8-pack-mount-" + strings.ReplaceAll(filepath.Base(jobRoot), "_", "-")
	script := strings.Join([]string{
		`touch "$P/new.md" 2>&1; echo "create=$?"`,
		`sh -c 'echo x >> "$P/WORKFLOW.md"' 2>&1; echo "append=$?"`,
		`chmod 0644 "$P/WORKFLOW.md" 2>&1; echo "chmod=$?"`,
		`mv "$P/WORKFLOW.md" "$P/renamed.md" 2>&1; echo "rename=$?"`,
		`rm -f "$P/WORKFLOW.md" 2>&1; echo "unlink=$?"`,
		`mkdir "$P/skills/x" 2>&1; echo "mkdir=$?"`,
		`echo ok > "$C/written.txt" 2>&1; echo "checkout=$?"`,
	}, "\n")
	command, args, err := Docker{}.Wrap("env", []string{"P=" + pack, "C=" + checkout, "sh", "-c", script}, p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("container run failed: %v\n%s", err, output)
	}
	text := string(output)
	t.Log(text)
	for _, operation := range []string{"create", "append", "chmod", "rename", "unlink", "mkdir"} {
		if strings.Contains(text, operation+"=0") {
			t.Fatalf("%s inside the Agent Pack succeeded:\n%s", operation, text)
		}
	}
	if count := strings.Count(text, "Read-only file system"); count < 6 {
		t.Fatalf("expected EROFS for all six operations, got %d:\n%s", count, text)
	}
	if !strings.Contains(text, "checkout=0") {
		t.Fatalf("checkout write failed:\n%s", text)
	}
	if body, err := os.ReadFile(filepath.Join(checkout, "written.txt")); err != nil || string(body) != "ok\n" {
		t.Fatalf("checkout write did not reach the host: %q %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(pack, "WORKFLOW.md")); err != nil || string(body) != "# workflow" {
		t.Fatalf("pack content changed: %q %v", body, err)
	}
}
