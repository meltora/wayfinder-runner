package validation

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TokenBudget(budget v1.TokenBudget) error {
	if budget.RunLimitTokens == nil && budget.ProjectRemainingTokens == nil {
		return fmt.Errorf("token_budget must set run_limit_tokens or project_remaining_tokens")
	}
	if budget.RunLimitTokens != nil && (*budget.RunLimitTokens == 0 || *budget.RunLimitTokens > v1.ProviderUsageMaxCount) {
		return fmt.Errorf("token_budget run_limit_tokens is out of range")
	}
	if budget.ProjectRemainingTokens != nil && *budget.ProjectRemainingTokens > v1.ProviderUsageMaxCount {
		return fmt.Errorf("token_budget project_remaining_tokens is out of range")
	}
	return nil
}
