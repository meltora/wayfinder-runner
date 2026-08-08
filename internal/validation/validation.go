package validation

import (
	"encoding/hex"
	"fmt"
	"math"
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
	if job.Kind != v1.JobKindPlan && job.Kind != v1.JobKindBuild {
		return fmt.Errorf("unsupported job kind %q", job.Kind)
	}
	if job.FencingToken == 0 {
		return fmt.Errorf("fencing_token must be non-zero")
	}
	if job.FencingToken > math.MaxInt64 {
		return fmt.Errorf("fencing_token exceeds supported durable range")
	}
	if len(job.Repositories) < 1 || len(job.Repositories) > 16 {
		return fmt.Errorf("job must contain between 1 and 16 repositories")
	}
	seenRepositories := map[string]bool{}
	primaryCount := 0
	for _, repository := range job.Repositories {
		if err := ID("repository_id", repository.RepositoryID); err != nil {
			return err
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
	if err := boundedText("ticket.summary", job.Ticket.Summary, 1024, true); err != nil {
		return err
	}
	if err := boundedText("ticket.description", job.Ticket.Description, 256<<10, false); err != nil {
		return err
	}
	if err := boundedText("ticket.acceptance_criteria", job.Ticket.AcceptanceCriteria, 128<<10, false); err != nil {
		return err
	}
	if err := boundedText("review_comment", job.ReviewComment, 128<<10, false); err != nil {
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
	if job.ResumeSessionID != "" {
		if err := ID("resume_session_id", job.ResumeSessionID); err != nil {
			return err
		}
	}
	if len(job.RequiredChecks) > 64 {
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
	if job.AgentPack != nil {
		if err := ID("agent_pack.key_id", job.AgentPack.KeyID); err != nil {
			return err
		}
		if err := boundedText("agent_pack.version", job.AgentPack.Version, 128, true); err != nil {
			return err
		}
		if len(job.AgentPack.Digest) != 64 {
			return fmt.Errorf("agent_pack.digest must be a sha256 digest")
		}
		if _, err := hex.DecodeString(job.AgentPack.Digest); err != nil || job.AgentPack.Digest != strings.ToLower(job.AgentPack.Digest) {
			return fmt.Errorf("agent_pack.digest must be a lowercase sha256 digest")
		}
		if len(job.AgentPack.URL) == 0 || len(job.AgentPack.URL) > 4096 {
			return fmt.Errorf("agent_pack.url has an invalid length")
		}
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
