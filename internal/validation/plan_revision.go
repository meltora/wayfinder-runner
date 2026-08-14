package validation

import (
	"encoding/json"
	"fmt"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	maxPlanFieldBytes       = 128 << 10
	maxPlanItemBytes        = 16 << 10
	maxPlanItems            = 128
	maxPlanContentBytes     = 768 << 10
	maxFeedbackBytes        = 256 << 10
	maxFeedbackSummaryBytes = 32 << 10
	maxFeedbackItemBytes    = 8 << 10
	maxFeedbackItems        = 64
	maxRevisionContextBytes = 1200 << 10
)

func PlanRevisionContext(context v1.PlanRevisionContext) error {
	if err := ID("plan_revision_context.request_id", context.RequestID); err != nil {
		return err
	}
	if err := SHA256Digest("plan_revision_context.request_digest", context.RequestDigest); err != nil {
		return err
	}
	if context.RequestedAt.IsZero() {
		return fmt.Errorf("plan_revision_context.requested_at is required")
	}
	if err := planRevisionFeedback(context.Feedback); err != nil {
		return err
	}
	if err := ID("plan_revision_context.base_plan.revision_id", context.BasePlan.RevisionID); err != nil {
		return err
	}
	if context.BasePlan.Revision < 1 {
		return fmt.Errorf("plan_revision_context.base_plan.revision must be positive")
	}
	if err := SHA256Digest("plan_revision_context.base_plan.digest", context.BasePlan.Digest); err != nil {
		return err
	}
	if err := PlanContent(context.BasePlan.Content); err != nil {
		return fmt.Errorf("plan_revision_context.base_plan.content: %w", err)
	}
	return aggregateJSONLimit("plan_revision_context", context, maxRevisionContextBytes)
}

func planRevisionFeedback(feedback v1.PlanRevisionFeedback) error {
	if err := nonEmptyText("plan_revision_context.feedback.summary", feedback.Summary, maxFeedbackSummaryBytes); err != nil {
		return err
	}
	if len(feedback.RequestedChanges) < 1 {
		return fmt.Errorf("plan_revision_context.feedback.requested_changes requires at least one item")
	}
	if feedback.Questions == nil {
		return fmt.Errorf("plan_revision_context.feedback.questions is required")
	}
	if err := textItems("plan_revision_context.feedback.requested_changes", feedback.RequestedChanges, maxFeedbackItems, maxFeedbackItemBytes, true); err != nil {
		return err
	}
	if err := textItems("plan_revision_context.feedback.questions", feedback.Questions, maxFeedbackItems, maxFeedbackItemBytes, true); err != nil {
		return err
	}
	return aggregateJSONLimit("plan_revision_context.feedback", feedback, maxFeedbackBytes)
}

func PlanContent(content v1.PlanContent) error {
	required := map[string]string{
		"behavior": content.Behavior, "architecture": content.Architecture,
		"api_impact": content.APIImpact, "data_impact": content.DataImpact,
		"security_impact": content.SecurityImpact, "operational_impact": content.OperationalImpact,
		"preview_requirements": content.PreviewRequirements, "automated_test_strategy": content.AutomatedTestStrategy,
		"manual_test_plan": content.ManualTestPlan, "rollout_strategy": content.RolloutStrategy,
		"rollback_strategy": content.RollbackStrategy,
	}
	for field, value := range required {
		if err := nonEmptyText("plan."+field, value, maxPlanFieldBytes); err != nil {
			return err
		}
	}
	lists := map[string][]string{
		"in_scope": content.InScope, "out_of_scope": content.OutOfScope,
		"assumptions": content.Assumptions, "unanswered_questions": content.UnansweredQuestions,
		"risks": content.Risks, "alternatives": content.Alternatives,
		"dependencies": content.Dependencies, "delivery_order": content.DeliveryOrder,
	}
	for field, values := range lists {
		if values == nil {
			return fmt.Errorf("plan.%s is required", field)
		}
		if err := textItems("plan."+field, values, maxPlanItems, maxPlanItemBytes, false); err != nil {
			return err
		}
	}
	return aggregateJSONLimit("plan", content, maxPlanContentBytes)
}

func nonEmptyText(field, value string, limit int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return boundedText(field, value, limit, true)
}

func textItems(field string, values []string, maximumItems, maximumBytes int, nonEmpty bool) error {
	if len(values) > maximumItems {
		return fmt.Errorf("%s exceeds %d items", field, maximumItems)
	}
	for index, value := range values {
		if nonEmpty && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s[%d] must not be empty", field, index)
		}
		if err := boundedText(fmt.Sprintf("%s[%d]", field, index), value, maximumBytes, false); err != nil {
			return err
		}
	}
	return nil
}

func aggregateJSONLimit(field string, value any, maximumBytes int) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", field, err)
	}
	if len(raw) > maximumBytes {
		return fmt.Errorf("%s exceeds %d-byte aggregate limit", field, maximumBytes)
	}
	return nil
}
