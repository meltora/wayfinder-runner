package isolation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
)

const loginRefreshTimeout = 2 * time.Minute

type LoginRefresh struct {
	RunnerID     string
	Network      string
	RefreshToken string
	Scopes       []string
}

func (o DockerOwnership) RefreshLogin(ctx context.Context, in LoginRefresh) ([]byte, error) {
	if hostRoot(o.EffectiveUID) {
		return nil, ErrHostRoot
	}
	if err := validateContainerImage(o.Image); err != nil {
		return nil, err
	}
	if !runnerLabelPattern.MatchString(in.RunnerID) {
		return nil, fmt.Errorf("the login refresh requires runner identity")
	}
	if !runnerLabelPattern.MatchString(in.Network) || in.Network == "host" || in.Network == "none" || in.Network == "bridge" {
		return nil, fmt.Errorf("the login refresh requires the restricted job network")
	}
	if o.StateDir == "" || !filepath.IsAbs(o.StateDir) || filepath.Clean(o.StateDir) != o.StateDir {
		return nil, fmt.Errorf("the login refresh requires an absolute clean state directory")
	}
	envFile, err := loginRefreshEnvFile(in.RefreshToken, in.Scopes)
	if err != nil {
		return nil, err
	}
	exe, err := o.helperExecutable()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("name the login refresh helper: %w", err)
	}
	attemptID := "login-refresh-" + hex.EncodeToString(nonce)
	dir, err := os.MkdirTemp(o.StateDir, "login-refresh-")
	if err != nil {
		return nil, fmt.Errorf("create the login refresh directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	envPath := filepath.Join(dir, "grant.env")
	if err := os.WriteFile(envPath, envFile, 0o600); err != nil {
		return nil, fmt.Errorf("write the login refresh grant: %w", err)
	}
	name := ContainerName(in.RunnerID, attemptID)
	args := []string{
		"run", "--rm", "--user", strconv.Itoa(JobUserID) + ":" + strconv.Itoa(JobGroupID), "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--network", in.Network, "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--log-driver", "none",
		"--pids-limit", "256", "--memory", "512m", "--name", name,
		"--label", "com.ourplatform.runner-id=" + in.RunnerID,
		"--label", "com.ourplatform.attempt-id=" + attemptID,
		"--env-file", envPath,
		"-v", exe + ":" + loginExchangeHelperBinary + ":ro",
		"--entrypoint", loginExchangeHelperBinary,
		o.Image, "claude-login-refresh",
	}
	runCtx, cancel := context.WithTimeout(ctx, loginRefreshTimeout)
	defer cancel()
	stdout, err := o.runIO(runCtx, name, args, nil)
	if err != nil {
		return nil, fmt.Errorf("refresh the Claude login: %w", err)
	}
	return bytes.TrimSpace(stdout), nil
}

func loginRefreshEnvFile(refreshToken string, scopes []string) ([]byte, error) {
	if err := claudelogin.CheckRefreshGrant(refreshToken, scopes); err != nil {
		return nil, fmt.Errorf("the login refresh grant is unusable")
	}
	return []byte(claudelogin.RefreshTokenEnv + "=" + refreshToken + "\n" +
		claudelogin.RefreshScopeEnv + "=" + strings.Join(scopes, " ") + "\n"), nil
}
