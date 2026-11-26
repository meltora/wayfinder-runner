package isolation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
)

const (
	loginExchangeHelperBinary = "/opt/our-runner-helper/our-runner"
	loginExchangeTimeout      = 30 * time.Second
)

var errLoginExchangeReplyTooLarge = errors.New("the login exchange reply is too large")

func (o DockerOwnership) ExchangeLogin(ctx context.Context, runnerID, attemptID, home string, in claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error) {
	request := OwnershipRequest{RunnerID: runnerID, AttemptID: attemptID, Purpose: "login-exchange", Private: []string{home}}
	name, _, err := o.validateHelper(request)
	if err != nil {
		return claudelogin.ExchangeResult{}, err
	}
	if rel, err := filepath.Rel(o.StateDir, home); err != nil || !strings.HasPrefix(filepath.ToSlash(rel), "job-homes/") {
		return claudelogin.ExchangeResult{}, fmt.Errorf("the login exchange only runs on a job home")
	}
	exe, err := o.helperExecutable()
	if err != nil {
		return claudelogin.ExchangeResult{}, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return claudelogin.ExchangeResult{}, fmt.Errorf("encode the login exchange request: %w", err)
	}
	args := []string{
		"run", "--rm", "-i", "--user", strconv.Itoa(JobUserID) + ":" + strconv.Itoa(JobGroupID), "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--network", "none", "--read-only", "--log-driver", "none",
		"--pids-limit", "64", "--memory", "128m", "--name", name,
		"--label", "com.ourplatform.runner-id=" + runnerID,
		"--label", "com.ourplatform.attempt-id=" + attemptID,
		"-v", home + ":" + ContainerHome + ":rw",
		"-v", exe + ":" + loginExchangeHelperBinary + ":ro",
		"--entrypoint", loginExchangeHelperBinary,
		o.Image, "claude-login-exchange",
	}
	runCtx, cancel := context.WithTimeout(ctx, loginExchangeTimeout)
	defer cancel()
	stdout, err := o.runIO(runCtx, name, args, stdin)
	if err != nil {
		return claudelogin.ExchangeResult{}, fmt.Errorf("exchange the job's Claude login: %w", err)
	}
	result, err := claudelogin.DecodeExchangeResult(bytes.TrimSpace(stdout))
	if err != nil {
		return claudelogin.ExchangeResult{}, fmt.Errorf("the login exchange helper sent an invalid reply")
	}
	return result, nil
}

func (o DockerOwnership) helperExecutable() (string, error) {
	lookup := o.Executable
	if lookup == nil {
		lookup = func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(exe)
		}
	}
	exe, err := lookup()
	if err != nil {
		return "", fmt.Errorf("find the runner executable for the login exchange: %w", err)
	}
	if !filepath.IsAbs(exe) || filepath.Clean(exe) != exe || strings.ContainsAny(exe, ":,\x00\r\n") {
		return "", fmt.Errorf("the runner executable path cannot be mounted into the login exchange helper")
	}
	return exe, nil
}

func (o DockerOwnership) runIO(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	if o.RunIO != nil {
		stdout, err := o.RunIO(ctx, name, args, stdin)
		if err != nil {
			return nil, errors.New("the login exchange helper failed")
		}
		if len(stdout) > claudelogin.MaxExchangeBytes {
			return nil, errLoginExchangeReplyTooLarge
		}
		return stdout, nil
	}
	// #nosec G204 -- executable is Docker, flags are fixed, the home is a validated job home and the runner path is checked before it is mounted.
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnvironment()
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := &limitedReply{limit: claudelogin.MaxExchangeBytes}
	var stderr cappedProbeBuffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		removeErr := removeContainers(cleanupCtx, []string{name})
		cleanupCancel()
		if removeErr != nil {
			return nil, fmt.Errorf("%w: %s (helper cleanup: %v)", err, strings.TrimSpace(stderr.String()), removeErr)
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow {
		return nil, errLoginExchangeReplyTooLarge
	}
	return stdout.data.Bytes(), nil
}

type limitedReply struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (r *limitedReply) Write(p []byte) (int, error) {
	if r.data.Len()+len(p) > r.limit {
		r.overflow = true
		return len(p), nil
	}
	return r.data.Write(p)
}
