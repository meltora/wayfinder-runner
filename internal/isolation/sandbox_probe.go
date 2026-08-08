package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProbeNestedSandbox verifies the complete outer-container/inner-sandbox
// boundary without using real provider credentials. The probe must be run with
// the exact immutable image that will execute jobs.
func ProbeNestedSandbox(ctx context.Context, stateDir, image string) error {
	probeRoot := filepath.Join(stateDir, "doctor")
	if err := os.MkdirAll(probeRoot, 0o700); err != nil {
		return fmt.Errorf("create sandbox probe root: %w", err)
	}
	dir, err := os.MkdirTemp(probeRoot, "sandbox-")
	if err != nil {
		return fmt.Errorf("create sandbox probe directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	home := filepath.Join(dir, "home")
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{\"test_only\":true}\n"), 0o600); err != nil {
		return err
	}
	if err := writeCodexProfiles(home); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workspace, "probe-input"), []byte("readable\n"), 0o600); err != nil {
		return err
	}

	attemptID := "sandbox-" + filepath.Base(dir)
	policy := Policy{
		MemoryMB:      512,
		CPUs:          1,
		PIDs:          128,
		NetworkNone:   true,
		ReadOnlyFS:    true,
		WorkDir:       workspace,
		WorkspaceRoot: workspace,
		JobHome:       home,
		Image:         image,
		RunnerID:      "doctor",
		AttemptID:     attemptID,
		NestedSandbox: true,
	}
	command, args, err := (Docker{}).Wrap("codex", []string{
		"sandbox", "-p", "runner-build", "-P", "runner-job", "-C", workspace, "--",
		"sh", "-ceu", "test -r ./probe-input; printf passed > ./probe-output; if cat /home/runner/.codex/auth.json >/dev/null 2>&1; then exit 42; fi",
	}, policy)
	if err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// #nosec G204 -- executable is Docker and all variable values are validated
	// configuration or runner-created private paths passed as positional argv.
	cmd := exec.CommandContext(probeCtx, command, args...)
	cmd.Env = DockerEnvironment()
	var output cappedProbeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	cleanupErr := RemoveJobContainer(cleanupCtx, policy.RunnerID, policy.AttemptID)
	cleanupCancel()
	if runErr != nil {
		return errors.Join(fmt.Errorf("nested sandbox probe failed: %w: %s", runErr, strings.TrimSpace(output.String())), cleanupErr)
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	workspaceRoot, err := os.OpenRoot(workspace)
	if err != nil {
		return fmt.Errorf("open sandbox probe workspace: %w", err)
	}
	defer func() { _ = workspaceRoot.Close() }()
	data, err := workspaceRoot.ReadFile("probe-output")
	if err != nil || string(data) != "passed" {
		return fmt.Errorf("nested sandbox did not produce its expected workspace output")
	}
	return nil
}

type cappedProbeBuffer struct {
	data []byte
}

func (b *cappedProbeBuffer) Write(p []byte) (int, error) {
	const limit = 32 << 10
	written := len(p)
	if len(b.data) < limit {
		remaining := limit - len(b.data)
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return written, nil
}

func (b *cappedProbeBuffer) String() string { return string(b.data) }
