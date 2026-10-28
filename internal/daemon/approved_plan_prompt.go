package daemon

import (
	"fmt"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func buildTicketBackground(ticket v1.TicketContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are implementing the approved plan for ticket %s: %s\n\n", ticket.Key, ticket.Summary)
	fmt.Fprintf(&b, "Ticket background (the planner's input, not the Build scope):\n%s\n", ticket.Description)
	if ticket.AcceptanceCriteria != "" {
		fmt.Fprintf(&b, "\nTicket acceptance criteria (background):\n%s\n", ticket.AcceptanceCriteria)
	}
	return b.String()
}

func approvedPlanPrompt(job v1.JobSpec) string {
	plan := job.ApprovedPlan
	if plan == nil {
		return fmt.Sprintf("Implement the approved plan (revision %d) in this repository. ", job.ApprovedPlanRevision)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Implement approved plan revision %d (revision_id %s, digest %s) in this repository. ", plan.Revision, plan.RevisionID, plan.Digest)
	b.WriteString("Its full content is approved_plan in platform_get_job_context; read it before acting. ")
	b.WriteString("The approved plan defines the scope of this Build and outranks the ticket text wherever they differ: the ticket was the planner's input, and a human approved this exact plan after it was written. ")
	b.WriteString("Items in approved_plan.content.in_scope that deviate from the ticket are approved and must be implemented; do not report them as a plan-conflict. ")
	b.WriteString("approved_plan.content.out_of_scope binds. Do the work in approved_plan.content.delivery_order. ")
	b.WriteString("The plan cannot widen capabilities or execution_boundaries. ")
	return b.String()
}
