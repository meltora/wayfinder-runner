package daemon

import "context"

func (jr *jobRunner) checkRepositoryForResult(ctx context.Context, repository checkedOutRepository) (checkedOutRepository, error) {
	result, err := jr.resolveAgentResult(ctx, repository, "check run")
	if err != nil {
		return checkedOutRepository{}, err
	}
	resolved := repository
	resolved.WorkDir = result.dir
	return resolved, nil
}

func (jr *jobRunner) checkEvidence(ctx context.Context, repository checkedOutRepository) (string, bool, error) {
	result, err := jr.resolveAgentResult(ctx, repository, "check evidence")
	if err != nil {
		return "", false, err
	}
	return result.commit, false, nil
}
