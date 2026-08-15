package supervisor

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func forwardSessionEnvironment(entry string) bool {
	key, _, ok := strings.Cut(entry, "=")
	if !ok {
		return false
	}
	if strings.HasPrefix(key, "PLATFORM_") || strings.HasPrefix(key, "RUNNER_") || strings.HasPrefix(key, "CODEX_") {
		return true
	}
	switch key {
	case "PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "TERM",
		"DOCKER_HOST", "DOCKER_CONTEXT", "XDG_RUNTIME_DIR", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
		"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB",
		"OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR",
		"OPENCODE_DISABLE_AUTOUPDATE", "OPENCODE_DISABLE_SHARE",
		"OPENCODE_DISABLE_LSP_DOWNLOAD", "OPENCODE_DISABLE_DEFAULT_PLUGINS",
		"OPENCODE_DISABLE_PROJECT_CONFIG":
		return true
	}
	return false
}

type Spec struct {
	Name       string
	Command    string
	Args       []string
	Dir        string
	Env        []string
	Stdout     io.Writer
	Stderr     io.Writer
	RuntimeDir string
	// ManagedConfigMounts are runner-generated, read-only provider policy files.
	// The production isolator applies a fixed target allowlist before mounting.
	ManagedConfigMounts []ManagedConfigMount
}

type ManagedConfigMount struct {
	Source string
	Target string
}

type Process interface {
	PID() int
	Wait() error
	Interrupt() error
	Kill() error
}

type SessionReporter interface {
	SessionRef() (string, error)
}

type Supervisor interface {
	Name() string
	Available() bool
	Start(ctx context.Context, spec Spec) (Process, error)
}

type DirectProcess struct {
	LookPath func(string) (string, error)
}

func NewDirectProcess() *DirectProcess { return &DirectProcess{LookPath: exec.LookPath} }

func (d *DirectProcess) Name() string    { return "direct" }
func (d *DirectProcess) Available() bool { return true }

type directProc struct{ cmd *exec.Cmd }

func (p *directProc) PID() int    { return p.cmd.Process.Pid }
func (p *directProc) Wait() error { return p.cmd.Wait() }
func (p *directProc) Interrupt() error {
	return signalProcessGroup(p.cmd.Process.Pid, sigInterrupt())
}
func (p *directProc) Kill() error {
	if err := signalProcessGroup(p.cmd.Process.Pid, sigKill()); err != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func (d *DirectProcess) Start(ctx context.Context, spec Spec) (Process, error) {
	if _, err := d.LookPath(spec.Command); err != nil {
		return nil, fmt.Errorf("%s not found: %w", spec.Command, err)
	}
	// #nosec G204 -- production receives the fixed docker wrapper; provider binaries are administrator-owned and argv is positional.
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	setProcGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := signalProcessGroup(cmd.Process.Pid, sigKill()); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &directProc{cmd: cmd}, nil
}
