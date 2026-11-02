package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/validation"
)

const (
	trustedResultsDirectory = "job-results"
	maxTrustedResultBytes   = 512 << 20
	maxTrustedGitOutput     = 64 << 20
	gitlinkMode             = "160000"
)

var trustedRepositoryConfig = [][2]string{
	{"core.bare", "false"},
	{"core.hooksPath", "/dev/null"},
	{"core.commitGraph", "false"},
	{"core.fsmonitor", "false"},
	{"core.untrackedCache", "false"},
	{"core.excludesFile", "/dev/null"},
	{"core.attributesFile", "/dev/null"},
	{"core.autocrlf", "false"},
	{"core.symlinks", "true"},
	{"core.ignoreCase", "false"},
	{"core.protectHFS", "true"},
	{"core.protectNTFS", "true"},
	{"gc.auto", "0"},
	{"maintenance.auto", "false"},
	{"commit.gpgSign", "false"},
	{"protocol.allow", "never"},
	{"submodule.recurse", "false"},
}

type trustedResultRepository struct {
	root               string
	gitDir             string
	filesDir           string
	sparseGitDir       string
	sparseTree         string
	date               string
	entryLimit         int
	byteLimit          int64
	depthLimit         int
	pathLimit          int
	resolveTimeout     time.Duration
	gitlinks           map[string]bool
	gitlinkCommits     map[string]string
	declaredSubmodules map[string]bool
	racyWindow         time.Duration
	cacheLimit         int
	caches             map[string]*candidateCache
	cacheClock         uint64
	cacheSerial        int
	mu                 sync.Mutex
}

type trustedGitOptions struct {
	gitDir   string
	workTree string
	stdin    io.Reader
	env      []string
	limit    int
}

func trustedResultsRoot(stateDir, workDir string) string {
	return filepath.Join(stateDir, trustedResultsDirectory, filepath.Base(workDir))
}

func (jr *jobRunner) prepareTrustedResultRepository(ctx context.Context, rm *repo.Manager, repositoryID, url, startSHA string) (*trustedResultRepository, error) {
	dest := filepath.Join(trustedResultsRoot(jr.d.cfg.StateDir, jr.workDir), repositoryID)
	if err := rm.CloneTrustedRepository(ctx, url, startSHA, filepath.Join(dest, "repo.git")); err != nil {
		_ = os.RemoveAll(dest)
		return nil, fmt.Errorf("prepare trusted result repository: %w", err)
	}
	trusted, err := configureTrustedResultRepository(ctx, dest, startSHA, time.Now())
	if err != nil {
		_ = os.RemoveAll(dest)
		return nil, fmt.Errorf("prepare trusted result repository: %w", err)
	}
	return trusted, nil
}

func configureTrustedResultRepository(ctx context.Context, dest, startSHA string, now time.Time) (*trustedResultRepository, error) {
	if err := validation.GitCommitSHA("start_sha", startSHA); err != nil {
		return nil, err
	}
	trusted := &trustedResultRepository{
		root:           dest,
		gitDir:         filepath.Join(dest, "repo.git"),
		filesDir:       filepath.Join(dest, "files"),
		sparseGitDir:   filepath.Join(dest, "sparse.git"),
		sparseTree:     filepath.Join(dest, "sparse-tree"),
		date:           "@" + strconv.FormatInt(now.Unix(), 10) + " +0000",
		entryLimit:     scanner.MaxGitTreeEntries,
		byteLimit:      maxTrustedResultBytes,
		depthLimit:     maxAgentResultDepth,
		pathLimit:      maxAgentResultPathBytes,
		resolveTimeout: agentResultResolveTimeout,
		racyWindow:     candidateRacyWindow,
		cacheLimit:     maxCandidateCaches,
	}
	for _, setting := range trustedRepositoryConfig {
		if _, err := trusted.gitAt(ctx, trustedGitOptions{gitDir: trusted.gitDir}, "config", setting[0], setting[1]); err != nil {
			return nil, err
		}
	}
	if err := writeTrustedExclude(trusted.gitDir, ""); err != nil {
		return nil, err
	}
	for _, dir := range []string{trusted.filesDir, trusted.sparseTree} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if _, err := trusted.gitAt(ctx, trustedGitOptions{}, "init", "--bare", "--quiet", trusted.sparseGitDir); err != nil {
		return nil, err
	}
	for _, setting := range trustedRepositoryConfig {
		if _, err := trusted.gitAt(ctx, trustedGitOptions{gitDir: trusted.sparseGitDir}, "config", setting[0], setting[1]); err != nil {
			return nil, err
		}
	}
	resolved, err := trusted.git(ctx, "rev-parse", "--verify", "--quiet", startSHA+"^{commit}")
	if err != nil || strings.TrimSpace(string(resolved)) != startSHA {
		return nil, fmt.Errorf("trusted result repository does not contain the start commit")
	}
	commits, err := trusted.treeGitlinkCommits(ctx, startSHA)
	if err != nil {
		return nil, err
	}
	declared, err := trusted.submodulePaths(ctx, startSHA)
	if err != nil {
		return nil, err
	}
	trusted.gitlinks, trusted.gitlinkCommits, trusted.declaredSubmodules = map[string]bool{}, commits, map[string]bool{}
	for gitlink := range commits {
		trusted.gitlinks[gitlink] = true
		if declared[gitlink] {
			trusted.declaredSubmodules[gitlink] = true
		}
	}
	return trusted, nil
}

func writeTrustedExclude(gitDir, agentPatterns string) error {
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return err
	}
	content := agentPatterns
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += runnerArtifactsExcludeEntry + "\n"
	return os.WriteFile(filepath.Join(infoDir, "exclude"), []byte(content), 0o600)
}

func (t *trustedResultRepository) treeGitlinks(ctx context.Context, commit string) (map[string]bool, error) {
	commits, err := t.treeGitlinkCommits(ctx, commit)
	if err != nil {
		return nil, err
	}
	gitlinks := make(map[string]bool, len(commits))
	for gitlink := range commits {
		gitlinks[gitlink] = true
	}
	return gitlinks, nil
}

func (t *trustedResultRepository) treeGitlinkCommits(ctx context.Context, commit string) (map[string]string, error) {
	listing, err := t.git(ctx, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	gitlinks := map[string]string{}
	for _, record := range strings.Split(string(listing), "\x00") {
		header, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if ok && len(fields) == 3 && fields[0] == gitlinkMode {
			gitlinks[path] = fields[2]
		}
	}
	return gitlinks, nil
}

func (t *trustedResultRepository) git(ctx context.Context, args ...string) ([]byte, error) {
	return t.gitAt(ctx, trustedGitOptions{gitDir: t.gitDir, workTree: t.filesDir}, args...)
}

func (t *trustedResultRepository) gitAt(ctx context.Context, options trustedGitOptions, args ...string) ([]byte, error) {
	command := t.command(ctx, options, args...)
	limit := options.limit
	if limit == 0 {
		limit = maxTrustedGitOutput
	}
	stdout := cappedBuffer{limit: limit}
	stderr := cappedBuffer{limit: 4096}
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Stdin = options.stdin
	if err := command.Run(); err != nil {
		return nil, trustedGitError(args, err, stderr.String())
	}
	if stdout.truncated {
		return nil, fmt.Errorf("trusted Git %s output exceeds %d bytes", gitSubcommand(args), limit)
	}
	return stdout.data, nil
}

func (t *trustedResultRepository) command(ctx context.Context, options trustedGitOptions, args ...string) *exec.Cmd {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.commitGraph=false"}
	if options.gitDir != "" {
		base = append([]string{"--git-dir=" + options.gitDir}, base...)
	}
	if options.workTree != "" {
		base = append([]string{"--work-tree=" + options.workTree}, base...)
	}
	// #nosec G204 -- git is fixed and every argument is runner-owned; no shell is used.
	command := exec.CommandContext(ctx, "git", append(base, args...)...)
	command.Dir = t.root
	if options.workTree != "" {
		command.Dir = options.workTree
	}
	command.Env = append(trustedGitEnvironment(t.root), options.env...)
	configureProcessTree(command)
	return command
}

func trustedGitEnvironment(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
	}
}

type trustedGitFailure struct {
	subcommand string
	stderr     string
	err        error
}

func (f *trustedGitFailure) Error() string {
	var exitErr *exec.ExitError
	if errors.As(f.err, &exitErr) && f.stderr != "" {
		return fmt.Sprintf("trusted Git %s failed: %s", f.subcommand, f.stderr)
	}
	return fmt.Sprintf("trusted Git %s failed: %v", f.subcommand, f.err)
}

func (f *trustedGitFailure) Unwrap() error { return f.err }

func trustedGitError(args []string, err error, stderr string) error {
	message := strings.TrimSpace(stderr)
	if len(message) > 512 {
		message = message[:512]
	}
	return &trustedGitFailure{subcommand: gitSubcommand(args), stderr: message, err: err}
}

func gitSubcommand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return "command"
}

func (t *trustedResultRepository) updateBranch(ctx context.Context, branch, commit string) error {
	if err := validation.GitRef("branch", branch); err != nil {
		return err
	}
	_, err := t.git(ctx, "update-ref", "refs/heads/"+branch, commit)
	return err
}

func removeTrustedResults(stateDir, workDir string) error {
	return errors.Join(
		removeRunnerTree(trustedResultsRoot(stateDir, workDir)),
		removeRunnerTree(checkWorkspaceRoot(stateDir, workDir)),
		removeRunnerTree(npmCacheRoot(stateDir, workDir)),
	)
}

func removeRunnerTree(target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := safeio.MakeTreeRemovable(target); err != nil {
			return err
		}
	}
	return os.RemoveAll(target)
}
