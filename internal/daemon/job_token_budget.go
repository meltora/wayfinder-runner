package daemon

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/provider"
)

type tokenBudgetFailure struct {
	code    v1.JobFinishedFailureCode
	message string
}

func (jr *jobRunner) tokenBudgetStartFailure() (tokenBudgetFailure, bool) {
	budget := jr.job.TokenBudget
	if budget == nil || budget.ProjectRemainingTokens == nil || *budget.ProjectRemainingTokens > 0 {
		return tokenBudgetFailure{}, false
	}
	return tokenBudgetFailure{
		code:    v1.JobFailureProjectTokenBudget,
		message: "project token budget was already used up before the provider started",
	}, true
}

func (jr *jobRunner) enforceTokenBudget(usage *provider.UsageAccumulator) {
	budget := jr.job.TokenBudget
	if budget == nil {
		return
	}
	used, ok := usage.BudgetedTokens()
	if !ok {
		return
	}
	failure, exceeded := tokenBudgetExceeded(*budget, used)
	if !exceeded {
		return
	}
	jr.procMu.Lock()
	if jr.tokenBudgetFailure != nil {
		jr.procMu.Unlock()
		return
	}
	jr.tokenBudgetFailure = &failure
	proc := jr.proc
	jr.procMu.Unlock()
	jr.logger.Warn("stopping provider at token budget", "failure_code", string(failure.code), "provider_reported_tokens", used)
	if proc != nil {
		_ = proc.Kill()
	}
}

func tokenBudgetExceeded(budget v1.TokenBudget, used uint64) (tokenBudgetFailure, bool) {
	runReached := budget.RunLimitTokens != nil && used >= *budget.RunLimitTokens
	projectReached := budget.ProjectRemainingTokens != nil && used >= *budget.ProjectRemainingTokens
	switch {
	case projectReached && (!runReached || *budget.ProjectRemainingTokens < *budget.RunLimitTokens):
		return tokenBudgetFailure{
			code: v1.JobFailureProjectTokenBudget,
			message: fmt.Sprintf("project token budget reached: %d tokens were left for this period and the provider reported %d",
				*budget.ProjectRemainingTokens, used),
		}, true
	case runReached:
		return tokenBudgetFailure{
			code:    v1.JobFailureRunTokenBudget,
			message: fmt.Sprintf("run token budget reached: limit %d tokens, provider reported %d", *budget.RunLimitTokens, used),
		}, true
	default:
		return tokenBudgetFailure{}, false
	}
}

func (jr *jobRunner) currentTokenBudgetFailure() (tokenBudgetFailure, bool) {
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	if jr.tokenBudgetFailure == nil {
		return tokenBudgetFailure{}, false
	}
	return *jr.tokenBudgetFailure, true
}
