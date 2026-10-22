package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/example/app-runner/internal/validation"
)

func (jr *jobRunner) publishAgentResult(ctx context.Context, repository checkedOutRepository) (agentResult, error) {
	result, err := jr.resolveAgentResult(ctx, repository, "build result")
	if err != nil {
		return agentResult{}, err
	}
	if err := jr.refuseCommittedRunnerArtifacts(ctx, repository, result.commit); err != nil {
		return agentResult{}, err
	}
	if err := refuseCommittedGitlinks(ctx, repository, result.commit); err != nil {
		return agentResult{}, err
	}
	if err := repository.Trusted.updateBranch(ctx, repository.Branch, result.commit); err != nil {
		return agentResult{}, err
	}
	return result, nil
}

func (jr *jobRunner) refuseCommittedRunnerArtifacts(ctx context.Context, repository checkedOutRepository, tip string) error {
	if repository.Trusted == nil {
		return agentResultRefusal(repository.ID, "the trusted result repository is not available")
	}
	if err := validation.GitCommitSHA("artifact_check_base_sha", repository.BaseSHA); err != nil {
		return fmt.Errorf("refusing to publish repository %s: %w", repository.ID, err)
	}
	if err := validation.GitCommitSHA("artifact_check_tip_sha", tip); err != nil {
		return fmt.Errorf("refusing to publish repository %s: %w", repository.ID, err)
	}
	changed, err := repository.Trusted.git(ctx, "log", "-z", "-m", "--name-only", "--no-renames",
		"--ignore-submodules=none", "--format=", repository.BaseSHA+".."+tip)
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(string(changed), "\x00") {
		if runnerArtifactPath(strings.TrimLeft(entry, "\n")) {
			return fmt.Errorf("refusing to publish %s/ content in repository %s", runnerArtifactsDirectory, repository.ID)
		}
	}
	return nil
}

func refuseCommittedGitlinks(ctx context.Context, repository checkedOutRepository, tip string) error {
	if err := validation.GitCommitSHA("gitlink_check_base_sha", repository.BaseSHA); err != nil {
		return fmt.Errorf("refusing to publish repository %s: %w", repository.ID, err)
	}
	raw, err := repository.Trusted.git(ctx, "diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-abbrev",
		"--ignore-submodules=none", repository.BaseSHA, tip)
	if err != nil {
		return err
	}
	if rawDiffAddsGitlink(string(raw)) {
		return agentResultRefusal(repository.ID, "the result adds or changes a gitlink")
	}
	return nil
}

func rawDiffAddsGitlink(raw string) bool {
	fields := strings.Split(raw, "\x00")
	for index := 0; index < len(fields); index += 2 {
		header := strings.TrimLeft(fields[index], "\n")
		if header == "" {
			break
		}
		modes := strings.Fields(strings.TrimPrefix(header, ":"))
		if !strings.HasPrefix(header, ":") || len(modes) < 2 || modes[1] == gitlinkMode {
			return true
		}
	}
	return false
}
