package validation

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	maxIDLength          = 128
	maxDisplayNameLength = 200
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ID validates an opaque identifier before it can reach a filesystem, socket,
// branch name, database key, or log field.
func ID(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxIDLength || !utf8.ValidString(value) || !idPattern.MatchString(value) {
		return fmt.Errorf("%s must be 1-%d characters using letters, numbers, '.', '_' or '-'", field, maxIDLength)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("%s is not a valid opaque identifier", field)
	}
	return nil
}

// DisplayName validates user-facing metadata. Storage code must still create
// its own filename instead of using this value as a path.
func DisplayName(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxDisplayNameLength || !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8 and at most %d bytes", field, maxDisplayNameLength)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("%s must be a filename, not a path", field)
	}
	return nil
}

// JobSpec validates command-controlled fields before the runner allocates a
// lease, creates a socket, or touches the filesystem.
func JobSpec(job v1.JobSpec) error {
	if err := ID("job_id", job.JobID); err != nil {
		return err
	}
	if err := ID("attempt_id", job.AttemptID); err != nil {
		return err
	}
	if err := ID("project_id", job.ProjectID); err != nil {
		return err
	}
	if job.Kind != v1.JobKindPlan && job.Kind != v1.JobKindBuild {
		return fmt.Errorf("unsupported job kind %q", job.Kind)
	}
	if err := ID("lease_id", job.LeaseID); err != nil {
		return err
	}
	if err := ID("manifest_id", job.ManifestID); err != nil {
		return err
	}
	if err := SHA256Digest("manifest_digest", job.ManifestDigest); err != nil {
		return err
	}
	if job.WorkflowRevision < 0 {
		return fmt.Errorf("workflow_revision must be non-negative")
	}
	if job.FencingToken == 0 {
		return fmt.Errorf("fencing_token must be non-zero")
	}
	if job.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("fencing_token exceeds the protocol JSON-safe range")
	}
	if len(job.Repositories) < 1 || len(job.Repositories) > 16 {
		return fmt.Errorf("job must contain between 1 and 16 repositories")
	}
	seenRepositories := map[string]bool{}
	primaryCount := 0
	for index, repository := range job.Repositories {
		if err := ID("repository_id", repository.RepositoryID); err != nil {
			return err
		}
		if repository.RepositoryHeadObservationID != "" {
			if err := ID("repository_head_observation_id", repository.RepositoryHeadObservationID); err != nil {
				return err
			}
		}
		if repository.BaseSHA != "" {
			if err := GitCommitSHA("base_sha", repository.BaseSHA); err != nil {
				return err
			}
		}
		if job.Kind == v1.JobKindPlan {
			if repository.RepositoryHeadObservationID == "" || repository.BaseSHA == "" || repository.BaseBranch == "" {
				return fmt.Errorf("plan repository authority requires repository_head_observation_id, base_branch, and base_sha")
			}
			if index == 0 && !repository.Primary {
				return fmt.Errorf("the first plan repository must be primary")
			}
		}
		if seenRepositories[repository.RepositoryID] {
			return fmt.Errorf("repository %q appears more than once", repository.RepositoryID)
		}
		seenRepositories[repository.RepositoryID] = true
		if repository.Primary {
			primaryCount++
		}
		if repository.BaseBranch != "" {
			if err := GitRef("base_branch", repository.BaseBranch); err != nil {
				return err
			}
		}
	}
	if primaryCount != 1 {
		return fmt.Errorf("job must define exactly one primary repository")
	}
	if err := ID("ticket.key", job.Ticket.Key); err != nil {
		return err
	}
	if err := boundedText("ticket.summary", job.Ticket.Summary, MaxTicketSummaryBytes, true); err != nil {
		return err
	}
	if err := boundedText("ticket.description", job.Ticket.Description, MaxTicketDescriptionBytes, false); err != nil {
		return err
	}
	if err := boundedText("ticket.acceptance_criteria", job.Ticket.AcceptanceCriteria, MaxAcceptanceCriteriaBytes, false); err != nil {
		return err
	}
	if err := boundedText("review_comment", job.ReviewComment, MaxReviewCommentBytes, false); err != nil {
		return err
	}
	if err := boundedText("pr_title", job.PRTitle, 512, false); err != nil {
		return err
	}
	if job.ApprovedPlanRevision < 0 || job.ContinuationCount < 0 || job.ContinuationCount > 100 {
		return fmt.Errorf("job revision or continuation count is invalid")
	}
	if job.Kind == v1.JobKindBuild && job.ApprovedPlanRevision < 1 {
		return fmt.Errorf("build job requires a positive approved_plan_revision")
	}
	if job.Kind == v1.JobKindBuild && job.PlanRevisionContext != nil {
		return fmt.Errorf("build job cannot contain plan_revision_context")
	}
	if err := ApprovedPlan(job); err != nil {
		return err
	}
	if job.PlanRevisionContext != nil {
		if err := PlanRevisionContext(*job.PlanRevisionContext); err != nil {
			return err
		}
	}
	if job.ResumeSessionID != "" {
		if err := ID("resume_session_id", job.ResumeSessionID); err != nil {
			return err
		}
	}
	if job.ProviderCapacityResumePolicy != "" &&
		job.ProviderCapacityResumePolicy != v1.CapacityResumeManual &&
		job.ProviderCapacityResumePolicy != v1.CapacityResumeAutomatic {
		return fmt.Errorf("provider_capacity_resume_policy must be manual or automatic")
	}
	if job.TokenBudget != nil {
		if err := TokenBudget(*job.TokenBudget); err != nil {
			return err
		}
	}
	if job.AgentRunAuthority != nil {
		if err := AgentRunAuthority(*job.AgentRunAuthority); err != nil {
			return err
		}
	}
	if job.AgentActivity != nil {
		if err := AgentActivityPolicy(*job.AgentActivity); err != nil {
			return err
		}
	}
	if job.AgentGuidance != nil {
		if err := AgentGuidance(*job.AgentGuidance); err != nil {
			return err
		}
	}
	if job.RunGuardrails != nil {
		if err := RunGuardrailsPolicy(*job.RunGuardrails); err != nil {
			return err
		}
	}
	if job.ElevatedCapabilities != nil {
		if err := ElevatedCapabilities(*job.ElevatedCapabilities); err != nil {
			return err
		}
	}
	switch job.ProviderAuthMode {
	case "", v1.ProviderAuthModeSubscription, v1.ProviderAuthModeAPIKey:
	default:
		return fmt.Errorf("provider_auth_mode %q is not supported", job.ProviderAuthMode)
	}
	if err := WorkAttachments(job.WorkAttachments); err != nil {
		return err
	}
	if len(job.RequiredChecks) > MaxRequiredChecks {
		return fmt.Errorf("job exceeds required-check limit")
	}
	seenChecks := map[string]bool{}
	for _, check := range job.RequiredChecks {
		if err := ID("required_check", check); err != nil {
			return err
		}
		if seenChecks[check] {
			return fmt.Errorf("required check %q appears more than once", check)
		}
		seenChecks[check] = true
	}
	if (job.AgentPack == nil) != (job.AgentPackAuthority == nil) {
		return fmt.Errorf("agent_pack and agent_pack_authority must be provided together")
	}
	if job.AgentPack != nil {
		if err := AgentPackReference(*job.AgentPack); err != nil {
			return err
		}
		if err := SignedAgentPackAuthority(*job.AgentPackAuthority); err != nil {
			return err
		}
		if !AgentPackAuthorityBindsReference(*job.AgentPackAuthority, *job.AgentPack) {
			return fmt.Errorf("signed Agent Pack authority must bind the exact Agent Pack ref")
		}
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	if len(raw) > (2<<20)-(8<<10) {
		return fmt.Errorf("job exceeds command payload limit")
	}
	return nil
}

func ManifestAuthority(authority v1.ManifestAuthority) error {
	if authority.Kind != v1.JobKindPlan && authority.Kind != v1.JobKindBuild {
		return fmt.Errorf("unsupported manifest kind %q", authority.Kind)
	}
	if err := ID("lease_id", authority.LeaseID); err != nil {
		return err
	}
	if err := ID("manifest_id", authority.ManifestID); err != nil {
		return err
	}
	if err := SHA256Digest("manifest_digest", authority.ManifestDigest); err != nil {
		return err
	}
	if authority.WorkflowRevision < 0 {
		return fmt.Errorf("workflow_revision must be non-negative")
	}
	return nil
}

func SHA256Digest(field, value string) error {
	if len(value) != 64 {
		return fmt.Errorf("%s must be a sha256 digest", field)
	}
	if _, err := hex.DecodeString(value); err != nil || value != strings.ToLower(value) {
		return fmt.Errorf("%s must be a lowercase sha256 digest", field)
	}
	return nil
}

func GitRef(field, value string) error {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.HasSuffix(value, "/") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "//") ||
		strings.ContainsAny(value, " ~^:?*[\\\x00") {
		return fmt.Errorf("%s is not a safe Git ref", field)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("%s is not a safe Git ref", field)
		}
	}
	return nil
}

// GitCommitSHA accepts only the complete lowercase object IDs supported by the
// protocol. Abbreviated or uppercase values are not stable recovery identity.
func GitCommitSHA(field, value string) error {
	if len(value) != 40 && len(value) != 64 {
		return fmt.Errorf("%s must be a full lowercase Git commit SHA", field)
	}
	if _, err := hex.DecodeString(value); err != nil || value != strings.ToLower(value) {
		return fmt.Errorf("%s must be a full lowercase Git commit SHA", field)
	}
	return nil
}

func boundedText(field, value string, limit int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > limit || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be valid UTF-8 and at most %d bytes", field, limit)
	}
	return nil
}

func WebURL(field, value string) error {
	if len(value) == 0 || len(value) > 4096 {
		return fmt.Errorf("%s has an invalid length", field)
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%s must be an absolute HTTPS URL without credentials or fragments", field)
	}
	return nil
}
