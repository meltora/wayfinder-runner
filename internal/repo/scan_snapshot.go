package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/validation"
)

// AgentConfigScanSupportedOS names platforms with the process-group cancellation
// guarantees required by this transport. Advertising and execution use one rule.
func AgentConfigScanSupportedOS(platform string) bool {
	return platform == "linux" || platform == "darwin"
}

var (
	ErrScanUnavailable  = errors.New("registered repository snapshot is unavailable")
	ErrScanInvalidInput = errors.New("registered repository scan input is invalid")
	ErrScanLimit        = scanner.ErrScanLimit
)

// ScanAgentConfig scans a trusted administrator-configured origin. Callers must
// resolve repository identity through runner configuration before calling it.
// A private, disposable bare clone isolates this read from job mirrors and Git
// configuration. No checkout, attributes, filters, hooks or submodules run.
func (m *Manager) ScanAgentConfig(ctx context.Context, registeredURL, branchRef string) (string, scanner.Inventory, error) {
	return m.ScanAgentConfigWithCredential(ctx, registeredURL, branchRef, nil)
}

type AgentConfigScanOptions struct {
	ContentMaxItemBytes int
}

func (m *Manager) ScanAgentConfigWithCredential(ctx context.Context, registeredURL, branchRef string, credential *GitHTTPSCredential) (string, scanner.Inventory, error) {
	return m.ScanAgentConfigWithOptions(ctx, registeredURL, branchRef, credential, AgentConfigScanOptions{})
}

func (m *Manager) ScanAgentConfigWithOptions(ctx context.Context, registeredURL, branchRef string, credential *GitHTTPSCredential, options AgentConfigScanOptions) (string, scanner.Inventory, error) {
	empty := scanner.Inventory{}
	if !AgentConfigScanSupportedOS(runtime.GOOS) {
		return "", empty, ErrScanUnavailable
	}
	if validateURL(registeredURL, m.AllowLocal) != nil || validation.AgentConfigScanBranch(branchRef) != nil {
		return "", empty, ErrScanInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", empty, err
	}
	parent := filepath.Join(m.Root, "agent-config-scans")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", empty, ErrScanUnavailable
	}
	dir, err := os.MkdirTemp(parent, "snapshot-")
	if err != nil {
		return "", empty, ErrScanUnavailable
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bare := filepath.Join(dir, "repository.git")
	branch := strings.TrimPrefix(branchRef, "refs/heads/")
	// A shallow single-branch clone negotiates the remote object format, including
	// SHA-256 repositories. --no-local prevents local test origins using hardlinks
	// or bypassing the requested shallow transport behavior.
	transport, err := m.scanTransport(registeredURL, credential)
	if err != nil {
		return "", empty, err
	}
	if _, err := m.scanRemoteGit(ctx, dir, transport, "clone", "--bare", "--no-local", "--depth=1", "--single-branch", "--no-tags", "--template=", "--branch", branch, "--", registeredURL, bare); err != nil {
		return "", empty, err
	}
	return m.scanPinnedOrigin(ctx, bare, registeredURL, branchRef, transport, options)
}

func (m *Manager) scanPinnedOrigin(ctx context.Context, bare, registeredURL, branchRef string, transport scanNetwork, options AgentConfigScanOptions) (string, scanner.Inventory, error) {
	empty := scanner.Inventory{}
	origin, err := m.scanGit(ctx, bare, 4096, "remote", "get-url", "origin")
	if err != nil {
		return "", empty, err
	}
	if strings.TrimSuffix(string(origin), "\n") != registeredURL {
		return "", empty, ErrScanUnavailable
	}
	// Fetch only the requested branch after origin verification. Neither clone's
	// HEAD nor a stale cached ref can substitute for a missing/deleted branch.
	if _, err := m.scanRemoteGit(ctx, bare, transport, "fetch", "--depth=1", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "origin", "+"+branchRef+":refs/heads/scan-input"); err != nil {
		return "", empty, err
	}
	rawSHA, err := m.scanGit(ctx, bare, 256, "rev-parse", "--verify", "refs/heads/scan-input^{commit}")
	if err != nil {
		return "", empty, err
	}
	sha := strings.TrimSuffix(string(rawSHA), "\n")
	if !validObjectID(sha) {
		return "", empty, ErrScanUnavailable
	}
	tree, err := m.scanGit(ctx, bare, scanner.MaxGitTreeBytes, "ls-tree", "-r", "-l", "-z", "--full-tree", sha)
	if err != nil {
		return "", empty, err
	}
	inventory, err := scanner.ScanGitTreeWithContents(ctx, tree, func(ctx context.Context, oid string) ([]byte, error) {
		if !validObjectID(oid) {
			return nil, scanner.ErrIncompleteScan
		}
		return m.scanGit(ctx, bare, scanner.MaxFileSize, "cat-file", "blob", oid)
	}, options.ContentMaxItemBytes)
	if err != nil {
		return "", empty, err
	}
	return sha, inventory, nil
}

type scanNetwork struct {
	host       string
	helper     string
	credential *GitHTTPSCredential
}

func (m *Manager) scanTransport(registeredURL string, credential *GitHTTPSCredential) (scanNetwork, error) {
	applicable, ok := applicableHTTPSCredential(registeredURL, credential)
	if !ok {
		return scanNetwork{}, nil
	}
	host, _ := credentialHost(registeredURL)
	helper, err := m.askPassExecutable()
	if err != nil {
		return scanNetwork{}, fmt.Errorf("%w: askpass helper unavailable", ErrScanUnavailable)
	}
	return scanNetwork{host: host, helper: helper, credential: applicable}, nil
}

func (m *Manager) scanRemoteGit(ctx context.Context, dir string, network scanNetwork, args ...string) ([]byte, error) {
	if network.credential != nil && !network.credential.usableAt(time.Now()) {
		return nil, fmt.Errorf("%w: %w", ErrScanUnavailable, ErrRepositoryCredentialUnavailable)
	}
	return m.runScanGit(ctx, dir, 4096, &network, args...)
}

func (m *Manager) scanGit(ctx context.Context, dir string, maxOutput int, args ...string) ([]byte, error) {
	return m.runScanGit(ctx, dir, maxOutput, nil, args...)
}

func (m *Manager) runScanGit(ctx context.Context, dir string, maxOutput int, network *scanNetwork, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	fixed := []string{"--no-pager", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "init.templateDir=", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "submodule.recurse=false", "-c", "fetch.recurseSubmodules=false", "-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true", "-c", "core.attributesFile=/dev/null", "-c", "credential.helper="}
	credentialed := network != nil && network.credential != nil
	if credentialed {
		fixed = append(fixed, "-c", "core.askPass="+network.helper, "-c", "http.followRedirects=false")
	}
	// #nosec G204 -- Git is fixed, arguments are individually validated and no shell is used.
	cmd := exec.CommandContext(commandCtx, "git", append(fixed, args...)...)
	cmd.Dir = dir
	protocols := "https:ssh"
	if m.AllowLocal {
		protocols += ":file"
	}
	if credentialed {
		protocols = "https"
	}
	cmd.Env = append(gitEnvironment(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_PROTOCOL_FROM_USER=0", "GIT_ALLOW_PROTOCOL="+protocols, "GIT_NO_REPLACE_OBJECTS=1", "GIT_LFS_SKIP_SMUDGE=1", "GIT_NO_LAZY_FETCH=1")
	if credentialed {
		cmd.Env = append(cmd.Env, credentialGitEnvironment(network.host, network.helper, *network.credential)...)
	}
	cmd.Env = append(cmd.Env, m.testGitEnvironment...)
	m.observeGit(cmd)
	stdout := scanOutput{limit: maxOutput, cancel: cancel}
	stderr := scanOutput{limit: 64 << 10, cancel: cancel}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	stopOwnedGroup := configureScanProcess(cmd)
	defer stopOwnedGroup()
	err := cmd.Start()
	if err == nil {
		stopOnCancel := context.AfterFunc(commandCtx, func() { _ = cmd.Cancel() })
		err = cmd.Wait()
		stopOnCancel()
	}
	if stdout.overflow || stderr.overflow {
		return nil, ErrScanLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		if network != nil {
			if sentinel := classifyGitAuthFailure(stderr.data, credentialed); sentinel != nil {
				return nil, fmt.Errorf("%w: %w: Git operation failed", ErrScanUnavailable, sentinel)
			}
		}
		return nil, fmt.Errorf("%w: Git operation failed", ErrScanUnavailable)
	}
	return stdout.data, nil
}

// Separate bounded streams preserve raw stdout and never include repository
// content, transport credentials or Git diagnostics in returned errors.
type scanOutput struct {
	data     []byte
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *scanOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-len(b.data) {
		b.overflow = true
		b.cancel()
		return 0, ErrScanLimit
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
