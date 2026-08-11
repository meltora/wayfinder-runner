package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/repo"
)

func (jr *jobRunner) finalizeBuild(
	ctx context.Context,
	rm *repo.Manager,
	publishReviewReady bool,
) (string, map[string]any, error) {
	job := jr.job
	extra := map[string]any{}
	commitSet := make(map[string]string, len(jr.repositories))
	for _, repository := range jr.repositories {
		sha, err := jr.commitAgentChanges(ctx, repository)
		if err != nil {
			return "failed", nil, fmt.Errorf("commit %s: %w", repository.ID, err)
		}
		commitSet[repository.ID] = sha
		if repository.Primary {
			extra["head_sha"] = sha
			extra["branch"] = repository.Branch
		}
	}
	extra["commit_set"] = commitSet

	checksPassed := jr.runRequiredChecks(ctx, commitSet)

	for _, repository := range jr.repositories {
		if err := jr.collectPatch(ctx, repository, commitSet[repository.ID]); err != nil {
			jr.logger.Warn("patch collection failed", "repository", repository.ID, "err", err)
		}
	}

	if job.PushBranch {
		for _, repository := range jr.repositories {
			if err := jr.pushBranch(ctx, repository, rm, commitSet[repository.ID]); err != nil {
				return "failed", nil, fmt.Errorf("push %s: %w", repository.ID, err)
			}
			if checksPassed && publishReviewReady {
				_ = jr.sendJobEvent(ctx, "pr_ready", map[string]any{
					"repository_id": repository.ID, "branch": repository.Branch,
					"head_sha": commitSet[repository.ID], "title": job.PRTitle,
				})
			}
		}
		extra["pushed"] = true
	}
	if !checksPassed {
		extra["error"] = "required checks failed"
		return "failed", extra, nil
	}
	return "succeeded", extra, nil
}

func (jr *jobRunner) commitAgentChanges(ctx context.Context, repository checkedOutRepository) (string, error) {
	status, _, err := jr.runWorkspaceCommand(ctx, "git-status-"+repository.ID, repository.WorkDir,
		"git", []string{"status", "--porcelain"}, false, 1<<20)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		if _, _, err := jr.runWorkspaceCommand(ctx, "git-add-"+repository.ID, repository.WorkDir,
			"git", []string{"add", "-A"}, false, 1<<20); err != nil {
			return "", err
		}
		msg := fmt.Sprintf("agent: implement %s %s", jr.job.Ticket.Key, jr.job.Ticket.Summary)
		if _, _, err := jr.runWorkspaceCommand(ctx, "git-commit-"+repository.ID, repository.WorkDir,
			"git", []string{"-c", "core.hooksPath=/dev/null", "-c", "user.name=our-runner",
				"-c", "user.email=runner@localhost", "commit", "-m", msg}, false, 1<<20); err != nil {
			return "", err
		}
	}
	sha, _, err := jr.runWorkspaceCommand(ctx, "git-head-"+repository.ID, repository.WorkDir,
		"git", []string{"rev-parse", "--verify", "HEAD^{commit}"}, false, 1<<20)
	return strings.TrimSpace(sha), err
}

func (jr *jobRunner) runRequiredChecks(ctx context.Context, commitSet map[string]string) bool {
	allOK := true
	for _, checkID := range jr.job.RequiredChecks {
		check, ok := jr.d.cfg.Checks[checkID]
		if !ok {
			_ = jr.sendJobEvent(ctx, "test_results", map[string]any{
				"suite": checkID, "check_id": checkID, "result": "error",
				"failures":   []string{"required check is not registered on this runner"},
				"commit_set": commitSet, "phase": "terminal", "source": "runner",
			})
			allOK = false
			continue
		}
		repository, ok := jr.checkRepository(check.RepositoryID)
		if !ok {
			allOK = false
			_ = jr.sendJobEvent(ctx, "test_results", map[string]any{
				"suite": checkID, "check_id": checkID, "result": "error",
				"failures":   []string{"check repository is not part of the job"},
				"commit_set": commitSet, "phase": "terminal", "source": "runner",
			})
			continue
		}
		outcome := jr.executeConfiguredCheck(ctx, checkID, repository, check)
		if outcome.Result != "passed" {
			allOK = false
		}
		event := map[string]any{
			"suite": checkID, "check_id": checkID,
			"result": outcome.Result, "duration_ms": outcome.Duration.Milliseconds(),
			"repository_id": repository.ID,
			"commit_sha":    commitSet[repository.ID], "commit_set": commitSet,
			"phase": "terminal", "source": "runner",
		}
		if outcome.Result != "passed" {
			failures := []string{}
			if outcome.Output != "" {
				failures = append(failures, outcome.Output)
			}
			event["failures"] = failures
			event["truncated"] = outcome.Truncated
		}
		if outcome.Class != "" {
			event["error_classification"] = outcome.Class
		}
		_ = jr.sendJobEvent(ctx, "test_results", event)
	}
	return allOK
}

func (jr *jobRunner) collectPatch(ctx context.Context, repository checkedOutRepository, sha string) error {
	diff, _, err := jr.runWorkspaceCommand(ctx, "git-diff-"+repository.ID, repository.WorkDir, "git",
		[]string{"--no-pager", "diff", "--no-ext-diff", "--no-textconv", repository.Base + "...HEAD"}, false, 64<<20)
	if err != nil || strings.TrimSpace(diff) == "" {
		return err
	}
	name := "changes-" + repository.ID + "-" + sha[:8] + ".patch"
	patchPath := filepath.Join(jr.workDir, name)
	if err := os.WriteFile(patchPath, []byte(diff), 0o600); err != nil {
		return err
	}
	_, err = jr.handlePublishArtifact(ctx, map[string]any{
		"name": name, "path": name, "content_type": "text/x-diff",
	})
	return err
}

func (jr *jobRunner) pushBranch(ctx context.Context, repository checkedOutRepository, rm *repo.Manager, sha string) error {
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return fmt.Errorf("lease validation before push: %w", err)
	}
	bundle, err := os.CreateTemp(jr.workDir, ".git-export-*.bundle")
	if err != nil {
		return err
	}
	bundlePath := bundle.Name()
	if err := bundle.Close(); err != nil {
		return err
	}
	if err := os.Remove(bundlePath); err != nil {
		return err
	}
	defer func() { _ = os.Remove(bundlePath) }()
	if _, _, err := jr.runWorkspaceCommand(ctx, "git-bundle-"+repository.ID, repository.WorkDir,
		"git", []string{"bundle", "create", bundlePath, repository.Branch}, false, 1<<20); err != nil {
		return err
	}
	if err := rm.ImportBundle(ctx, repository.URL, bundlePath, repository.Branch, sha); err != nil {
		return fmt.Errorf("import isolated Git bundle: %w", err)
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return fmt.Errorf("lease validation immediately before push: %w", err)
	}
	return rm.PushMirrorBranch(ctx, repository.URL, repository.Branch)
}

func (jr *jobRunner) checkRepository(id string) (checkedOutRepository, bool) {
	for _, repository := range jr.repositories {
		if (id == "" && repository.Primary) || repository.ID == id {
			return repository, true
		}
	}
	return checkedOutRepository{}, false
}

func (jr *jobRunner) runWorkspaceCommand(ctx context.Context, purpose, dir, command string, args []string, configuredNetwork bool, outputLimit int) (string, bool, error) {
	wrappedCommand := command
	wrappedArgs := append([]string(nil), args...)
	workDir := dir
	containerAttempt := jr.job.AttemptID + "-task-" + shortDigest(purpose)
	cleanup := func() error { return nil }
	commandEnvironment := allowedCheckEnvironment()
	if jr.d.cfg.Isolation == "docker" {
		iso, err := isolation.Select("docker", false)
		if err != nil {
			return "", false, err
		}
		homeRoot := filepath.Join(jr.d.cfg.StateDir, "check-homes")
		if err := os.MkdirAll(homeRoot, 0o700); err != nil {
			return "", false, err
		}
		home, err := os.MkdirTemp(homeRoot, "home-")
		if err != nil {
			return "", false, err
		}
		cleanup = func() error {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			containerErr := isolation.RemoveJobContainer(cleanupCtx, jr.d.runnerID, containerAttempt)
			homeErr := os.RemoveAll(home)
			if containerErr != nil {
				return containerErr
			}
			return homeErr
		}
		policy := isolation.Policy{
			MemoryMB: jr.d.cfg.JobMemoryMB, CPUs: jr.d.cfg.JobCPUs, PIDs: jr.d.cfg.JobPIDs,
			NetworkNone: !configuredNetwork || jr.d.cfg.JobNetwork == "none", NetworkName: jr.d.cfg.JobNetworkName,
			ReadOnlyFS: true, WorkDir: dir, WorkspaceRoot: jr.workDir, JobHome: home,
			Image: jr.d.cfg.JobImage, RunnerID: jr.d.runnerID, AttemptID: containerAttempt,
		}
		wrappedCommand, wrappedArgs, err = iso.Wrap(command, wrappedArgs, policy)
		if err != nil {
			_ = cleanup()
			return "", false, err
		}
		workDir = ""
		commandEnvironment = isolation.DockerEnvironment()
	}
	// #nosec G204 -- docker is fixed in production; direct mode is explicit unsafe development and admin checks use positional argv.
	cmd := exec.CommandContext(ctx, wrappedCommand, wrappedArgs...)
	configureProcessTree(cmd)
	cmd.Dir = workDir
	cmd.Env = commandEnvironment
	var output cappedBuffer
	output.limit = outputLimit
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	cleanupErr := cleanup()
	return output.String(), output.truncated, errors.Join(runErr, cleanupErr)
}

type cappedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		b.data = append(b.data, p...)
	} else {
		b.truncated = true
	}
	return written, nil
}

func (b *cappedBuffer) String() string {
	if b.truncated {
		return string(b.data) + "\n[output truncated]"
	}
	return string(b.data)
}

func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func allowedCheckEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"TMPDIR": true, "LANG": true, "LC_ALL": true,
	}
	out := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			out = append(out, entry)
		}
	}
	return out
}
