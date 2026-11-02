package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/app-runner/internal/config"
)

const (
	npmCacheSuffix             = ".npm-cache"
	maxNPMCacheBytes           = 4 << 30
	npmVersionTimeout          = 30 * time.Second
	maxInstallOutputBytes      = 1 << 20
	maxInstallMessageBytes     = 4096
	npmNotFoundExitCode        = 127
	installResultInstalled     = "installed"
	installResultRefused       = "refused"
	installResultFailed        = "failed"
	installResultTimeout       = "timeout"
	installResultCancelled     = "cancelled"
	installResultNotRun        = "not_run"
	installNetworkNone         = "none"
	installNetworkRestricted   = "restricted"
	installNetworkHost         = "host"
	checkWorkspacePlaceholder  = "<check workspace>"
	npmCachePlaceholder        = "<npm cache>"
	installCommandPurposeLabel = "install-"
)

type installReport struct {
	Ecosystem   string                   `json:"ecosystem"`
	Network     string                   `json:"network"`
	Offline     bool                     `json:"offline"`
	ToolVersion *string                  `json:"tool_version"`
	Argv        []string                 `json:"argv"`
	Directories []installDirectoryReport `json:"directories"`
}

type installDirectoryReport struct {
	Directory          string  `json:"directory"`
	Lockfile           *string `json:"lockfile"`
	LockfileBlobSHA    *string `json:"lockfile_blob_sha"`
	PackageJSONBlobSHA *string `json:"package_json_blob_sha"`
	Result             string  `json:"result"`
	DurationMS         int64   `json:"duration_ms"`
	Entries            *int    `json:"entries"`
	Bytes              *int64  `json:"bytes"`
	TreeDigest         *string `json:"tree_digest"`
}

type checkInstall struct {
	jr         *jobRunner
	ctx        context.Context
	request    checkRequest
	workspace  *checkWorkspace
	location   checkLocation
	commit     string
	report     *installReport
	refusalOut *checkOutcome
	started    time.Time

	versionDirectory string
}

func npmCacheRoot(stateDir, workDir string) string {
	return trustedResultsRoot(stateDir, workDir) + npmCacheSuffix
}

func (jr *jobRunner) npmInstallArgv() []string {
	argv := []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier"}
	if jr.offlineInstall() {
		argv = append(argv, "--offline")
	}
	return argv
}

func (jr *jobRunner) offlineInstall() bool {
	return jr.d.cfg.Isolation == "docker" && jr.d.cfg.JobNetwork == "none"
}

func (jr *jobRunner) installNetwork() string {
	switch {
	case jr.d.cfg.Isolation != "docker":
		return installNetworkHost
	case jr.d.cfg.JobNetwork == "none":
		return installNetworkNone
	default:
		return installNetworkRestricted
	}
}

func (jr *jobRunner) installCheckDependencies(ctx context.Context, request checkRequest, workspace *checkWorkspace, location checkLocation, commit string) (*installReport, *checkOutcome) {
	if request.check.Install == nil {
		return nil, nil
	}
	install := &checkInstall{
		jr: jr, ctx: ctx, request: request, workspace: workspace, location: location, commit: commit,
		report: newInstallReport(jr, request.check.Install), started: time.Now(),
	}
	install.run()
	return install.report, install.outcome()
}

func newInstallReport(jr *jobRunner, install *config.CheckInstallConfig) *installReport {
	report := &installReport{
		Ecosystem: install.Ecosystem, Network: jr.installNetwork(), Offline: jr.offlineInstall(),
		Argv: jr.npmInstallArgv(), Directories: []installDirectoryReport{},
	}
	for _, directory := range install.InstallDirectories() {
		report.Directories = append(report.Directories, installDirectoryReport{Directory: directory, Result: installResultNotRun})
	}
	return report
}

func (i *checkInstall) run() {
	inputs, ok := i.readInputs()
	if !ok {
		return
	}
	root, err := os.OpenRoot(i.location.root)
	if err != nil {
		i.fail("internal", i.message("could not be verified"))
		return
	}
	defer func() { _ = root.Close() }()
	repositoryRel, err := i.jr.checkRepositoryRel(i.request.repository)
	if err != nil {
		i.fail("internal", i.message("could not be verified"))
		return
	}
	scan := i.workspace.newInstalledTreeScan(i.ctx, root, i.request.checkID)
	guard := i.workspace.newInstallGuard(i.ctx, root, i.request.checkID)
	for index, input := range inputs {
		if !i.installDirectory(index, input, scan, guard, repositoryRel) {
			return
		}
	}
}

func (i *checkInstall) readInputs() ([]installInput, bool) {
	trusted := i.request.repository.Trusted
	if trusted == nil {
		i.fail("internal", i.message("could not read the published tree"))
		return nil, false
	}
	inputs := make([]installInput, 0, len(i.report.Directories))
	for index, directory := range i.report.Directories {
		input, err := readInstallInput(i.ctx, trusted, i.request.checkID, i.commit, directory.Directory, i.workspace.lockfileByteLimit)
		if err != nil {
			i.report.Directories[index].Result = installResultRefused
			var refusal *installRefusal
			switch {
			case errors.As(err, &refusal):
				i.fail("internal", refusal.message)
			case i.ctx.Err() != nil:
				i.report.Directories[index].Result = installResultCancelled
				i.fail(classifyInstallContext(i.ctx), i.message("was cancelled"))
			default:
				i.fail("internal", i.message("could not read the published tree"))
			}
			return nil, false
		}
		i.report.Directories[index].Lockfile = &input.lockfile
		i.report.Directories[index].LockfileBlobSHA = &input.lockfileBlobSHA
		i.report.Directories[index].PackageJSONBlobSHA = &input.packageJSONBlobSHA
		inputs = append(inputs, input)
	}
	return inputs, true
}

func (i *checkInstall) readToolVersion(dir, directory string) bool {
	if err := prepareNPMCache(i.workspace.npmCache, i.workspace.npmCacheByteLimit); err != nil {
		i.fail("internal", i.message("could not prepare the npm cache"))
		return false
	}
	versionCtx, cancel := context.WithTimeout(i.ctx, npmVersionTimeout)
	defer cancel()
	out, _, err := i.jr.runInstallCommand(versionCtx, i.request, dir, []string{"--version"}, i.workspace.npmCache, 4096)
	if err != nil {
		i.commandFailed(versionCtx, err, out, false, "npm --version", npmVersionTimeout)
		return false
	}
	version := strings.TrimSpace(out)
	parsed, ok := parseNPMVersion(version)
	if len(version) > 64 || !ok {
		i.fail("internal", i.message("failed: npm reported an unusable version"))
		return false
	}
	if i.report.ToolVersion != nil && *i.report.ToolVersion != version {
		i.fail("internal", i.message(fmt.Sprintf("needs one npm version; the check environment has npm %s in %s and npm %s in %s",
			*i.report.ToolVersion, displayInstallDirectory(i.versionDirectory), version, displayInstallDirectory(directory))))
		return false
	}
	i.report.ToolVersion, i.versionDirectory = &version, directory
	if !parsed.atLeast(minimumNPMVersion) {
		i.fail("internal", i.message("needs npm "+minimumNPMVersionID+" or later; the check environment has npm "+version))
		return false
	}
	return true
}

func (i *checkInstall) installDirectory(index int, input installInput, scan *installedTreeScan, guard *installGuard, repositoryRel string) bool {
	directory := &i.report.Directories[index]
	started := time.Now()
	defer func() { directory.DurationMS = time.Since(started).Milliseconds() }()
	before, err := guard.snapshot()
	if err != nil {
		directory.Result = i.verificationFailed(err)
		return false
	}
	dir := filepath.Join(i.location.dirs[i.request.repository.ID], filepath.FromSlash(input.directory))
	if !i.readToolVersion(dir, input.directory) {
		return false
	}
	if err := prepareNPMCache(i.workspace.npmCache, i.workspace.npmCacheByteLimit); err != nil {
		directory.Result = installResultFailed
		i.fail("internal", i.message("could not prepare the npm cache"))
		return false
	}
	timeout := time.Duration(i.request.check.Install.InstallTimeoutSeconds()) * time.Second
	installCtx, cancel := context.WithTimeout(i.ctx, timeout)
	defer cancel()
	out, capped, err := i.jr.runInstallCommand(installCtx, i.request, dir, i.report.Argv[1:], i.workspace.npmCache, maxInstallOutputBytes)
	if err != nil {
		directory.Result = i.commandFailed(installCtx, err, out, capped, "npm ci", timeout)
		return false
	}
	summary, err := scan.scan(repositoryRel, input.directory, input.installRoots)
	if err != nil {
		directory.Result = i.verificationFailed(err)
		return false
	}
	installRel := path.Join(repositoryRel, input.directory)
	skip := map[string]bool{}
	for _, nested := range append([]string{""}, input.installRoots...) {
		installed := path.Join(installRel, nested, installedDependencyDirectory)
		if _, existed := before[installed]; !existed {
			skip[installed] = true
		}
	}
	changed, found, err := guard.changed(before, skip)
	if err != nil {
		directory.Result = i.verificationFailed(err)
		return false
	}
	if found {
		directory.Result = installResultRefused
		display := strings.TrimPrefix(changed, repositoryRel+"/")
		if display == changed {
			display = checkWorkspacePlaceholder + "/" + changed
		}
		i.fail("internal", i.message(fmt.Sprintf("changed %s outside %s", display, path.Join(input.directory, installedDependencyDirectory))))
		return false
	}
	directory.Result = installResultInstalled
	directory.Entries, directory.Bytes, directory.TreeDigest = &summary.entries, &summary.bytes, &summary.digest
	return true
}

func (i *checkInstall) verificationFailed(err error) string {
	var refusal *installRefusal
	switch {
	case errors.As(err, &refusal):
		i.fail("internal", refusal.message)
	case i.ctx.Err() != nil:
		i.fail(classifyInstallContext(i.ctx), i.message("was cancelled"))
		return installResultCancelled
	default:
		i.fail("internal", i.message("could not be verified"))
	}
	return installResultRefused
}

func (i *checkInstall) commandFailed(ctx context.Context, err error, out string, capped bool, command string, timeout time.Duration) string {
	class := classifyCheckError(ctx, err)
	var exitErr *exec.ExitError
	switch {
	case class == "timeout":
		i.fail(class, i.message(fmt.Sprintf("took longer than %ds", int(timeout/time.Second))))
		return installResultTimeout
	case class == "cancelled":
		i.fail(class, i.message("was cancelled"))
		return installResultCancelled
	case errors.Is(err, exec.ErrNotFound) || (errors.As(err, &exitErr) && exitErr.ExitCode() == npmNotFoundExitCode):
		i.fail("internal", i.message("failed: npm is not available in the check environment"))
		return installResultFailed
	case errors.As(err, &exitErr):
		prefix := i.message(fmt.Sprintf("failed (%s exited %d): ", command, exitErr.ExitCode()))
		message, truncated := i.boundedOutputMessage(prefix, out)
		i.refusalOut = &checkOutcome{Result: "error", Class: "internal", Output: message, Truncated: truncated || capped}
		return installResultFailed
	default:
		i.jr.logger.Warn("dependency install could not be started", "check", i.request.checkID, "command", command, "err", err)
		i.fail("internal", i.message("failed: the install could not be started"))
		return installResultFailed
	}
}

func (i *checkInstall) boundedOutputMessage(prefix, out string) (string, bool) {
	cleaned := strings.ReplaceAll(out, i.location.root, checkWorkspacePlaceholder)
	cleaned = strings.ReplaceAll(cleaned, i.workspace.npmCache, npmCachePlaceholder)
	cleaned = redactCheckOutput(cleaned)
	message := prefix + cleaned
	if len(message) <= maxInstallMessageBytes {
		return message, false
	}
	return truncateUTF8Bytes(message, maxInstallMessageBytes), true
}

func (i *checkInstall) message(detail string) string {
	return "dependency install for check " + i.request.checkID + " " + detail
}

func (i *checkInstall) fail(class, message string) {
	i.refusalOut = &checkOutcome{Result: "error", Class: class, Output: message}
}

func (i *checkInstall) outcome() *checkOutcome {
	if i.refusalOut == nil {
		return nil
	}
	outcome := *i.refusalOut
	outcome.Duration = time.Since(i.started)
	return &outcome
}

func classifyInstallContext(ctx context.Context) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		return "cancelled"
	default:
		return "internal"
	}
}

func (jr *jobRunner) runInstallCommand(ctx context.Context, request checkRequest, dir string, args []string, npmCache string, outputLimit int) (string, bool, error) {
	return jr.runWorkspaceTask(ctx, workspaceTask{
		purpose: installCommandPurposeLabel + request.checkID, ownershipPurpose: "install",
		root: jr.checkWorkspace().root, dir: dir, command: "npm", args: args,
		configuredNetwork: true, outputLimit: outputLimit, npmCache: npmCache,
	})
}

func installEnvironment(base []string, npmCache string) []string {
	return append(append([]string(nil), base...), "npm_config_cache="+npmCache)
}

func prepareNPMCache(dir string, limit int64) error {
	size, err := regularFileBytes(dir)
	if err != nil || size > limit || !privateDirectory(dir) {
		if err := removeRunnerTree(dir); err != nil {
			return err
		}
	}
	return os.MkdirAll(dir, 0o700)
}

func privateDirectory(dir string) bool {
	info, err := os.Lstat(dir)
	return errors.Is(err, fs.ErrNotExist) || (err == nil && info.IsDir() && info.Mode().Perm() == 0o700)
}

func regularFileBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return total, err
}
