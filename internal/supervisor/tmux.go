package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TmuxSupervisor struct {
	Dir      string
	LookPath func(string) (string, error)
	run      func(cmd string, args ...string) error
}

func NewTmux(dir string) *TmuxSupervisor {
	t := &TmuxSupervisor{Dir: dir, LookPath: exec.LookPath}
	t.run = func(cmd string, args ...string) error {
		// #nosec G204 -- executable is fixed and every shell payload component is shellQuote encoded.
		c := exec.Command("tmux", args...)
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	return t
}

func (t *TmuxSupervisor) Name() string { return "tmux" }
func (t *TmuxSupervisor) Available() bool {
	lp := t.LookPath
	if lp == nil {
		lp = exec.LookPath
	}
	_, err := lp("tmux")
	return err == nil
}

type tmuxProc struct {
	session  string
	dir      string
	exitFile string
	run      func(cmd string, args ...string) error
	ctx      context.Context
	done     chan struct{}
	stopOnce sync.Once
}

func (t *TmuxSupervisor) Start(ctx context.Context, spec Spec) (Process, error) {
	if err := validateRedirectedStdinSource(spec); err != nil {
		return nil, err
	}
	if !t.Available() {
		return nil, fmt.Errorf("tmux not available")
	}
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(t.Dir, "job-")
	if err != nil {
		return nil, err
	}
	// #nosec G302 -- this is a private directory; owner traversal requires 0700.
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	session := sanitizeSession(spec.Name + "-" + filepath.Base(dir))
	logFile := filepath.Join(dir, "output.log")
	exitFile := filepath.Join(dir, "exitcode")

	var cmdStr strings.Builder
	for _, e := range spec.Env {
		if forwardSessionEnvironment(e) {
			fmt.Fprintf(&cmdStr, "export %s; ", shellQuote(e))
		}
	}
	for _, e := range spec.ProviderEnvironment {
		fmt.Fprintf(&cmdStr, "export %s; ", shellQuote(e))
	}
	fmt.Fprintf(&cmdStr, "cd %s && %s", shellQuote(spec.Dir), shellQuote(spec.Command))
	for _, a := range spec.Args {
		cmdStr.WriteString(" " + shellQuote(a))
	}
	cmdStr.WriteString(stdinRedirect(spec))
	fmt.Fprintf(&cmdStr, " > %s 2>&1; echo $? > %s", shellQuote(logFile), shellQuote(exitFile))

	if err := t.run("tmux", "new-session", "-d", "-s", session, cmdStr.String()); err != nil {
		return nil, err
	}
	return &tmuxProc{session: session, dir: dir, exitFile: exitFile, run: t.run, ctx: ctx, done: make(chan struct{})}, nil
}

func (p *tmuxProc) PID() int { return 0 }

func (p *tmuxProc) Wait() error {
	defer func() { _ = os.RemoveAll(p.dir) }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		case <-p.done:
			return context.Canceled
		case <-ticker.C:
			data, err := readExitCode(p.exitFile)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			code, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if code != 0 {
				return fmt.Errorf("exit status %d", code)
			}
			return nil
		}
	}
}

func (p *tmuxProc) Interrupt() error {
	return p.run("tmux", "send-keys", "-t", p.session, "C-c")
}

func (p *tmuxProc) Kill() error {
	p.stopOnce.Do(func() { close(p.done) })
	return p.run("tmux", "kill-session", "-t", p.session)
}

func sanitizeSession(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
