package daemon

import (
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const (
	maxAgentPromptBytes        = 1 << 20
	agentPromptTooLargeMessage = "agent prompt exceeds the runner size limit"
)

func agentInputWithinBounds(job v1.JobSpec, continuation *v1.ReviewFeedbackContinuationAuthority) bool {
	if len(job.Ticket.Summary) > validation.MaxTicketSummaryBytes ||
		len(job.Ticket.Description) > validation.MaxTicketDescriptionBytes ||
		len(job.Ticket.AcceptanceCriteria) > validation.MaxAcceptanceCriteriaBytes ||
		len(job.ReviewComment) > validation.MaxReviewCommentBytes ||
		len(job.RequiredChecks) > validation.MaxRequiredChecks {
		return false
	}
	for _, check := range job.RequiredChecks {
		if len(check) > validation.MaxRequiredCheckBytes {
			return false
		}
	}
	return continuation == nil || len(continuation.Feedback.Content) <= v1.ReviewFeedbackContinuationMaxContentBytes
}

func agentPromptWithinLimit(prompt string, limit int) bool {
	return len(prompt) <= limit
}

func (jr *jobRunner) agentPromptByteLimit() int {
	if jr.agentPromptLimit > 0 {
		return jr.agentPromptLimit
	}
	return maxAgentPromptBytes
}
