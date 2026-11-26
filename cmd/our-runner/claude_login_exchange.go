package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
	"github.com/example/app-runner/internal/isolation"
)

const claudeLoginExchangeDeadline = 25 * time.Second

func claudeLoginExchangeFailure(stderr io.Writer, reason string) int {
	fmt.Fprintln(stderr, "claude login exchange failed: "+reason)
	return 1
}

func runClaudeLoginExchange(args []string, stdin io.Reader, stdout, stderr io.Writer, home string) int {
	if len(args) != 0 {
		return claudeLoginExchangeFailure(stderr, "the command takes no arguments")
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, claudelogin.MaxExchangeBytes+1))
	if err != nil {
		return claudeLoginExchangeFailure(stderr, "the request could not be read")
	}
	request, err := claudelogin.DecodeExchangeRequest(raw)
	if err != nil {
		return claudeLoginExchangeFailure(stderr, "the request is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeLoginExchangeDeadline)
	defer cancel()
	result, err := claudelogin.Exchange(ctx, home, request, claudelogin.LockOptions{})
	switch {
	case errors.Is(err, claudelogin.ErrLocked):
		return claudeLoginExchangeFailure(stderr, "Claude Code holds the job login lock")
	case errors.Is(err, claudelogin.ErrUnparsable), errors.Is(err, claudelogin.ErrNoOAuth), errors.Is(err, claudelogin.ErrNoAccess), errors.Is(err, claudelogin.ErrBadExpiry):
		return claudeLoginExchangeFailure(stderr, "the host login in the request is unusable")
	case err != nil:
		return claudeLoginExchangeFailure(stderr, "the job login could not be updated")
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return claudeLoginExchangeFailure(stderr, "the result could not be written")
	}
	return 0
}

func cmdClaudeLoginExchange(args []string) int {
	return runClaudeLoginExchange(args, os.Stdin, os.Stdout, os.Stderr, isolation.ContainerHome)
}
