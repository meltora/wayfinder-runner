package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
)

const (
	claudeLoginRefreshDeadline  = 90 * time.Second
	claudeLoginRefreshConfigDir = "/tmp/claude-login"
)

type claudeAuthLogin func(ctx context.Context, configDir string) error

func claudeLoginRefreshFailure(stderr io.Writer, reason string) int {
	fmt.Fprintln(stderr, "claude login refresh failed: "+reason)
	return 1
}

func runClaudeLoginRefresh(args []string, stdout, stderr io.Writer, configDir string, login claudeAuthLogin) int {
	if len(args) != 0 {
		return claudeLoginRefreshFailure(stderr, "the command takes no arguments")
	}
	if os.Getenv(claudelogin.RefreshTokenEnv) == "" || os.Getenv(claudelogin.RefreshScopeEnv) == "" {
		return claudeLoginRefreshFailure(stderr, "the refresh grant is missing")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return claudeLoginRefreshFailure(stderr, "the config directory could not be created")
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeLoginRefreshDeadline)
	defer cancel()
	if err := login(ctx, configDir); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return claudeLoginRefreshFailure(stderr, "claude auth login exited with status "+strconv.Itoa(exit.ExitCode()))
		}
		return claudeLoginRefreshFailure(stderr, "claude auth login did not run")
	}
	data, refreshed, err := claudelogin.ReadLogin(configDir)
	if err != nil || !refreshed.HasRefresh {
		return claudeLoginRefreshFailure(stderr, "claude auth login saved no usable login")
	}
	if _, err := stdout.Write(data); err != nil {
		return claudeLoginRefreshFailure(stderr, "the login could not be written")
	}
	return 0
}

func runClaudeAuthLogin(ctx context.Context, configDir string) error {
	cmd := exec.CommandContext(ctx, "claude", "auth", "login")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir, "HOME=/tmp")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func cmdClaudeLoginRefresh(args []string) int {
	return runClaudeLoginRefresh(args, os.Stdout, os.Stderr, claudeLoginRefreshConfigDir, runClaudeAuthLogin)
}
