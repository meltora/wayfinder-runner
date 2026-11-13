package validation

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func uint64Pointer(value uint64) *uint64 { return &value }

func TestJobSpecAcceptsTokenBudget(t *testing.T) {
	t.Parallel()
	for name, budget := range map[string]v1.TokenBudget{
		"run only":          {RunLimitTokens: uint64Pointer(1)},
		"project only":      {ProjectRemainingTokens: uint64Pointer(0)},
		"both at max":       {RunLimitTokens: uint64Pointer(v1.ProviderUsageMaxCount), ProjectRemainingTokens: uint64Pointer(v1.ProviderUsageMaxCount)},
		"project exhausted": {RunLimitTokens: uint64Pointer(10), ProjectRemainingTokens: uint64Pointer(0)},
	} {
		job := validJob()
		budgetCopy := budget
		job.TokenBudget = &budgetCopy
		if err := JobSpec(job); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
}

func TestJobSpecRejectsInvalidTokenBudget(t *testing.T) {
	t.Parallel()
	for name, budget := range map[string]v1.TokenBudget{
		"empty":            {},
		"zero run limit":   {RunLimitTokens: uint64Pointer(0)},
		"run overflow":     {RunLimitTokens: uint64Pointer(v1.ProviderUsageMaxCount + 1)},
		"project overflow": {ProjectRemainingTokens: uint64Pointer(v1.ProviderUsageMaxCount + 1)},
	} {
		job := validJob()
		budgetCopy := budget
		job.TokenBudget = &budgetCopy
		if err := JobSpec(job); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestTokenBudgetDecodingRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	var budget v1.TokenBudget
	if err := json.Unmarshal([]byte(`{"run_limit_tokens":5,"currency":"usd"}`), &budget); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

func TestJobFinishedEventAcceptsTokenBudgetFailureCodes(t *testing.T) {
	t.Parallel()
	for _, code := range []v1.JobFinishedFailureCode{v1.JobFailureRunTokenBudget, v1.JobFailureProjectTokenBudget} {
		event := validJobFinishedEvent()
		event.State = v1.JobFinishedFailed
		event.FailureCode = code
		event.FailureMessage = "token budget reached"
		if err := JobFinishedEvent(event); err != nil {
			t.Fatalf("%s rejected: %v", code, err)
		}
	}
	if v1.JobFailureRunTokenBudget != "run_token_budget_exceeded" || v1.JobFailureProjectTokenBudget != "project_token_budget_exceeded" {
		t.Fatal("token budget failure code wire values changed")
	}
}
