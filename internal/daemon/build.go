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
	status, err := jr.runWorkspaceCommand(ctx, "git-status-"+repository.ID, repository.WorkDir,
		"git", []string{"status", "--porcelain"}, false, 1<<20)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		if _, err := jr.runWorkspaceCommand(ctx, "git-add-"+repository.ID, repository.WorkDir,
			"git", []string{"add", "-A"}, false, 1<<20); err != nil {
			return "", err
		}
		msg := fmt.Sprintf("agent: implement %s %s", jr.job.Ticket.Key, jr.job.Ticket.Summary)
		if _, err := jr.runWorkspaceCommand(ctx, "git-commit-"+repository.ID, repository.WorkDir,
			"git", []string{"-c", "core.hooksPath=/dev/null", "-c", "user.name=our-runner",
				"-c", "user.email=runner@localhost", "commit", "-m", msg}, false, 1<<20); err != nil {
			return "", err
		}
	}
	sha, err := jr.runWorkspaceCommand(ctx, "git-head-"+repository.ID, repository.WorkDir,
		"git", []string{"rev-parse", "--verify", "HEAD^{commit}"}, false, 1<<20)
	return strings.TrimSpace(sha), err
}

func (jr *jobRunner) runRequiredChecks(ctx context.Context, commitSet map[string]string) bool {
	allOK := true
	for _, checkID := range jr.job.RequiredChecks {
		start := time.Now()
		check, ok := jr.d.cfg.Checks[checkID]
		if !ok {
			_ = jr.sendJobEvent(ctx, "test_results", map[string]any{
				"suite": checkID, "command": checkID, "result": "error",
				"failures": []string{"required check is not registered on this runner"}, "commit_set": commitSet,
			})
			allOK = false
			continue
		}
		timeout := 10 * time.Minute
		if check.TimeoutSeconds > 0 {
			timeout = time.Duration(check.TimeoutSeconds) * time.Second
		}
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		repository, ok := jr.checkRepository(check.RepositoryID)
		if !ok {
			cancel()
			allOK = false
			_ = jr.sendJobEvent(ctx, "test_results", map[string]any{
				"suite": checkID, "result": "error",
				"failures": []string{"check repository is not part of the job"}, "commit_set": commitSet,
			})
			continue
		}
		out, runErr := jr.runCheck(checkCtx, checkID, repository.WorkDir, check.Command, check.Args)
		cancel()
		result := "passed"
		if runErr != nil {
			result = "failed"
			allOK = false
		}
		failures := []string{}
		if runErr != nil {
			failures = append(failures, truncate(out, 2000))
		}
		_ = jr.sendJobEvent(ctx, "test_results", map[string]any{
			"suite": checkID, "command": append([]string{check.Command}, check.Args...), "result": result,
			"duration_ms": time.Since(start).Milliseconds(),
			"failures":    failures, "repository_id": repository.ID,
			"commit_sha": commitSet[repository.ID], "commit_set": commitSet,
		})
	}
	return allOK
}

func (jr *jobRunner) collectPatch(ctx context.Context, repository checkedOutRepository, sha string) error {
	diff, err := jr.runWorkspaceCommand(ctx, "git-diff-"+repository.ID, repository.WorkDir, "git",
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
	if _, err := jr.runWorkspaceCommand(ctx, "git-bundle-"+repository.ID, repository.WorkDir,
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

func (jr *jobRunner) runCheck(ctx context.Context, checkID, dir, command string, args []string) (string, error) {
	out, err := jr.runWorkspaceCommand(ctx, "check-"+checkID, dir, command, args, true, 1<<20)
	return truncate(out, 8192), err
}

func (jr *jobRunner) runWorkspaceCommand(ctx context.Context, purpose, dir, command string, args []string, configuredNetwork bool, outputLimit int) (string, error) {
	wrappedCommand := command
	wrappedArgs := append([]string(nil), args...)
	workDir := dir
	containerAttempt := jr.job.AttemptID + "-task-" + shortDigest(purpose)
	cleanup := func() error { return nil }
	commandEnvironment := allowedCheckEnvironment()
	if jr.d.cfg.Isolation == "docker" {
		iso, err := isolation.Select("docker", false)
		if err != nil {
			return "", err
		}
		homeRoot := filepath.Join(jr.d.cfg.StateDir, "check-homes")
		if err := os.MkdirAll(homeRoot, 0o700); err != nil {
			return "", err
		}
		home, err := os.MkdirTemp(homeRoot, "home-")
		if err != nil {
			return "", err
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
			return "", err
		}
		workDir = ""
		commandEnvironment = isolation.DockerEnvironment()
	}
	// #nosec G204 -- docker is fixed in production; direct mode is explicit unsafe development and admin checks use positional argv.
	cmd := exec.CommandContext(ctx, wrappedCommand, wrappedArgs...)
	cmd.Dir = workDir
	cmd.Env = commandEnvironment
	var output cappedBuffer
	output.limit = outputLimit
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	cleanupErr := cleanup()
	return output.String(), errors.Join(runErr, cleanupErr)
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
