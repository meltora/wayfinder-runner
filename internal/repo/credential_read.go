package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/example/app-runner/internal/redact"
)

var (
	ErrRepositoryCredentialUnavailable = errors.New("repository credential unavailable")
	ErrRepositoryAccessDenied          = errors.New("repository access denied")
)

const redactedCredential = "[redacted]"

type GitHTTPSCredential struct {
	Username string
	Password string
	NotAfter time.Time
}

type PushCredential = GitHTTPSCredential

func (GitHTTPSCredential) String() string { return redactedCredential }

func (GitHTTPSCredential) GoString() string {
	return "repo.GitHTTPSCredential{" + redactedCredential + "}"
}

func (c GitHTTPSCredential) Format(state fmt.State, verb rune) {
	if verb == 'v' && state.Flag('#') {
		_, _ = io.WriteString(state, c.GoString())
		return
	}
	_, _ = io.WriteString(state, redactedCredential)
}

func (c GitHTTPSCredential) usableAt(now time.Time) bool {
	return c.Username != "" && c.Password != "" && (c.NotAfter.IsZero() || now.Before(c.NotAfter))
}

func applicableHTTPSCredential(remoteURL string, credential *GitHTTPSCredential) (*GitHTTPSCredential, bool) {
	if credential == nil {
		return nil, false
	}
	if _, ok := credentialHost(remoteURL); !ok {
		return nil, false
	}
	return credential, true
}

func credentialHost(remoteURL string) (string, bool) {
	parsed, err := url.Parse(remoteURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" {
		return "", false
	}
	return parsed.Host, true
}

var (
	credentialUnavailableMarkers = []string{
		"could not read Username for 'https://",
		"could not read Password for 'https://",
		"Authentication failed for 'https://",
		"The requested URL returned error: 401",
	}
	accessDeniedMarkers = []string{
		"Authentication failed for",
		"The requested URL returned error: 401",
		"The requested URL returned error: 403",
		"The requested URL returned error: 404",
		"Repository not found",
		"could not read Username for",
		"could not read Password for",
	}
	gitRepositoryNotFound = regexp.MustCompile(`(?m)^fatal: repository '[^'\n]*' not found$`)
)

func classifyGitAuthFailure(stderr []byte, credentialSupplied bool) error {
	if !credentialSupplied {
		if containsAny(stderr, credentialUnavailableMarkers) {
			return ErrRepositoryCredentialUnavailable
		}
		return nil
	}
	if containsAny(stderr, accessDeniedMarkers) || gitRepositoryNotFound.Match(stderr) {
		return ErrRepositoryAccessDenied
	}
	return nil
}

func containsAny(text []byte, markers []string) bool {
	for _, marker := range markers {
		if bytes.Contains(text, []byte(marker)) {
			return true
		}
	}
	return false
}

func credentialGitOptions(helperPath string) []string {
	return []string{
		"-c", "credential.helper=",
		"-c", "core.askPass=" + helperPath,
		"-c", "http.followRedirects=false",
		"-c", "core.hooksPath=/dev/null",
	}
}

func credentialGitEnvironment(host, helperPath string, credential GitHTTPSCredential) []string {
	return []string{
		"GIT_ASKPASS=" + helperPath,
		AskPassModeEnv + "=1",
		askPassHostEnv + "=" + host,
		askPassUserEnv + "=" + credential.Username,
		askPassSecretEnv + "=" + credential.Password,
		"LC_ALL=C",
		"LANGUAGE=",
	}
}

func (m *Manager) remoteGit() func(context.Context, string, string, GitHTTPSCredential, ...string) (string, error) {
	if m.RemoteGit != nil {
		return m.RemoteGit
	}
	return m.runCredentialGit
}

func (m *Manager) runCredentialGit(ctx context.Context, dir, remoteURL string, credential GitHTTPSCredential, args ...string) (string, error) {
	host, ok := credentialHost(remoteURL)
	if !ok {
		return "", fmt.Errorf("credential fetch requires an HTTPS origin without user information")
	}
	helper, err := m.askPassExecutable()
	if err != nil {
		return "", fmt.Errorf("credential fetch askpass helper unavailable")
	}
	// #nosec G204 -- executable is fixed; the URL and refs are validated and argv is never interpreted by a shell.
	cmd := exec.CommandContext(ctx, "git", append(credentialGitOptions(helper), args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitEnvironment(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_ALLOW_PROTOCOL=https",
	)
	cmd.Env = append(cmd.Env, credentialGitEnvironment(host, helper, credential)...)
	cmd.Env = append(cmd.Env, m.testGitEnvironment...)
	m.observeGit(cmd)
	var stdout, stderr limitedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		failure := fmt.Errorf("git %s: %w: %s", args[0], err, stderr.String())
		if sentinel := classifyGitAuthFailure(stderr.data, true); sentinel != nil {
			failure = fmt.Errorf("%w: %w", sentinel, failure)
		}
		return "", redact.Error(failure, credential.Password)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (m *Manager) observeGit(cmd *exec.Cmd) {
	if m.testGitObserver != nil {
		m.testGitObserver(cmd.Args[1:], cmd.Env)
	}
}

func (m *Manager) remoteFetch(ctx context.Context, dir, remoteURL string, credential *GitHTTPSCredential, existingMirror bool, run func() (string, error), args ...string) (string, error) {
	applicable, ok := applicableHTTPSCredential(remoteURL, credential)
	if !ok {
		out, err := run()
		if err != nil && classifyGitAuthFailure([]byte(err.Error()), false) != nil {
			return out, fmt.Errorf("%w: %w", ErrRepositoryCredentialUnavailable, err)
		}
		return out, err
	}
	if existingMirror {
		if err := m.checkCredentialMirrorConfig(ctx, remoteURL, "fetch"); err != nil {
			return "", err
		}
	}
	if !applicable.usableAt(time.Now()) {
		return "", ErrRepositoryCredentialUnavailable
	}
	return m.remoteGit()(ctx, dir, remoteURL, *applicable, args...)
}
