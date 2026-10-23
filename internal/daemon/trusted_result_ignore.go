package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type ignoreOracle struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	output  *bufio.Reader
	stderr  *cappedBuffer
}

func (t *trustedResultRepository) startIgnoreOracle(ctx context.Context, gitDir, workTree string, noIndex bool) (*ignoreOracle, error) {
	return t.startOracle(ctx, trustedGitOptions{gitDir: gitDir, workTree: workTree}, noIndex)
}

func (t *trustedResultRepository) startOracle(ctx context.Context, options trustedGitOptions, noIndex bool) (*ignoreOracle, error) {
	args := []string{"check-ignore", "--stdin", "-z", "-v", "-n"}
	if noIndex {
		args = append(args, "--no-index")
	}
	options.env = append(append([]string{}, options.env...), "GIT_FLUSH=1")
	command := t.command(ctx, options, args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &cappedBuffer{limit: 4096}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start trusted ignore check: %w", err)
	}
	return &ignoreOracle{command: command, stdin: stdin, output: bufio.NewReader(stdout), stderr: stderr}, nil
}

func (o *ignoreOracle) ignored(path string) (bool, error) {
	if _, err := io.WriteString(o.stdin, "./"+path+"\x00"); err != nil {
		return false, fmt.Errorf("trusted ignore check stopped: %w", err)
	}
	fields := make([]string, 4)
	for index := range fields {
		field, err := o.output.ReadString(0)
		if err != nil {
			return false, fmt.Errorf("trusted ignore check stopped: %w", err)
		}
		fields[index] = strings.TrimSuffix(field, "\x00")
	}
	pattern := fields[2]
	return pattern != "" && !strings.HasPrefix(pattern, "!"), nil
}

func (o *ignoreOracle) close() error {
	_ = o.stdin.Close()
	_, _ = io.Copy(io.Discard, o.output)
	err := o.command.Wait()
	var exitErr *exec.ExitError
	if err == nil || (errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
		return nil
	}
	return fmt.Errorf("trusted ignore check failed: %w: %s", err, strings.TrimSpace(o.stderr.String()))
}
