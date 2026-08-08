package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var minimalEnvironment = []string{"HOME=/nonexistent", "LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin"}

type ProcessCall struct {
	Path           string
	Args, Env      []string
	UID, GID       int
	DropPrivileges bool
	Dir            string
}
type ProcessRunner func(context.Context, ProcessCall) error

func runProcess(ctx context.Context, call ProcessCall) error {
	// #nosec G204 -- callers provide either the fixed systemctl path or one of
	// two exact binaries inside a root-owned, signature-verified staging slot.
	cmd := exec.CommandContext(ctx, call.Path, call.Args...)
	cmd.Env = append([]string(nil), call.Env...)
	cmd.Dir = call.Dir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	cmd.Stdin = nil
	if call.DropPrivileges {
		if call.UID <= 0 || call.GID <= 0 || int64(call.UID) > int64(^uint32(0)) || int64(call.GID) > int64(^uint32(0)) {
			return fmt.Errorf("bounded non-root credentials are required")
		}
		// #nosec G115 -- UID and GID are checked against uint32 bounds above.
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(call.UID), Gid: uint32(call.GID), Groups: []uint32{uint32(call.GID)}}, Setpgid: true}
		cmd.WaitDelay = 2 * time.Second
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
	}
	out := &boundedOutput{remaining: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Start()
	if err == nil {
		pid := cmd.Process.Pid
		err = cmd.Wait()
		// Only credential-dropped preflights are placed in a dedicated process
		// group. Never signal -pid for ordinary commands: after the child exits,
		// that process-group ID could belong to an unrelated service.
		if call.DropPrivileges {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(call.Path), err, strings.TrimSpace(string(out.data)))
	}
	return nil
}

type boundedOutput struct {
	data      []byte
	remaining int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	original := len(p)
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	b.data = append(b.data, p...)
	b.remaining -= len(p)
	return original, nil
}

type CommandPreflight struct {
	Timeout   time.Duration
	Run       ProcessRunner
	RunnerUID int
	RunnerGID int
}

func (p *CommandPreflight) Check(ctx context.Context, stage string) error {
	if p == nil {
		return fmt.Errorf("preflight is not configured")
	}
	timeout := p.Timeout
	if timeout <= 0 || timeout > 2*time.Minute {
		timeout = 30 * time.Second
	}
	run := p.Run
	if run == nil {
		run = runProcess
	}
	if p.RunnerUID <= 0 || p.RunnerGID <= 0 {
		return fmt.Errorf("non-root runner uid and gid are required")
	}
	for _, name := range requiredBinaries {
		path := filepath.Join(stage, name)
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		err := run(callCtx, ProcessCall{Path: path, Args: []string{"version"}, Env: minimalEnvironment, UID: p.RunnerUID, GID: p.RunnerGID, DropPrivileges: true, Dir: stage})
		cancel()
		if err != nil {
			return fmt.Errorf("preflight %s: %w", name, err)
		}
	}
	return nil
}

type SystemdController struct {
	Timeout time.Duration
	Run     ProcessRunner
	Output  func(context.Context, string, []string, []string) ([]byte, error)
}

func (s *SystemdController) command(ctx context.Context, verb, service string) error {
	if service != DefaultRunnerService {
		return fmt.Errorf("refusing non-default service")
	}
	timeout := s.Timeout
	if timeout <= 0 || timeout > 2*time.Minute {
		timeout = 30 * time.Second
	}
	run := s.Run
	if run == nil {
		run = runProcess
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return run(callCtx, ProcessCall{Path: "/usr/bin/systemctl", Args: []string{"--no-ask-password", "--no-pager", verb, service}, Env: minimalEnvironment})
}
func (s *SystemdController) Stop(ctx context.Context, service string) error {
	return s.command(ctx, "stop", service)
}
func (s *SystemdController) Start(ctx context.Context, service string) error {
	return s.command(ctx, "start", service)
}
func (s *SystemdController) MainPID(ctx context.Context, service string) (int, error) {
	if service != DefaultRunnerService {
		return 0, fmt.Errorf("refusing non-default service")
	}
	timeout := s.Timeout
	if timeout <= 0 || timeout > 2*time.Minute {
		timeout = 30 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output := s.Output
	if output == nil {
		output = func(ctx context.Context, path string, args, env []string) ([]byte, error) {
			// #nosec G204 -- this closure is invoked below with the fixed systemctl
			// path and a closed argument list; indirection exists only for tests.
			cmd := exec.CommandContext(ctx, path, args...)
			cmd.Env = append([]string(nil), env...)
			return cmd.Output()
		}
	}
	out, err := output(callCtx, "/usr/bin/systemctl", []string{"--no-ask-password", "--no-pager", "show", "--property=MainPID", "--value", service}, minimalEnvironment)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("runner service has no live MainPID")
	}
	return pid, nil
}
