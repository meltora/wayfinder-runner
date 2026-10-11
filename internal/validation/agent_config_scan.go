package validation

import (
	"fmt"
	v1 "github.com/example/app-runner/api/runner/v1"
	"regexp"
	"time"
)

var scanRequestIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var scanBranchPattern = regexp.MustCompile(`^refs/heads/[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func AgentConfigScanTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02T15:04:05Z", value)
	if err != nil || parsed.Format("2006-01-02T15:04:05Z") != value || parsed.IsZero() {
		return time.Time{}, fmt.Errorf("scan timestamp must be canonical UTC seconds")
	}
	return parsed, nil
}
func AgentConfigScanBranch(value string) error {
	if len(value) > 256 || !scanBranchPattern.MatchString(value) {
		return fmt.Errorf("scan requires a canonical full branch reference")
	}
	return GitRef("branch_ref", value)
}
func AgentConfigScanAuthority(a v1.AgentConfigScanAuthority) error {
	if a.AuthorityVersion != v1.AgentConfigScanTransportVersion || !scanRequestIDPattern.MatchString(a.RequestID) {
		return fmt.Errorf("scan authority version or request ID is invalid")
	}
	for field, value := range map[string]string{"attempt_id": a.AttemptID, "project_id": a.ProjectID, "repository_id": a.RepositoryID, "lease_id": a.LeaseID} {
		if err := ID(field, value); err != nil {
			return err
		}
	}
	if err := AgentConfigScanBranch(a.BranchRef); err != nil {
		return err
	}
	if a.FencingToken == 0 || a.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("scan fencing token is invalid")
	}
	if _, err := AgentConfigScanTimestamp(a.LeaseExpiresAt); err != nil {
		return err
	}
	if err := SHA256Digest("command_digest", a.CommandDigest); err != nil {
		return err
	}
	if a.CommandDigest != v1.AgentConfigScanCommandDigest(a) {
		return fmt.Errorf("scan command digest does not match authority")
	}
	return nil
}
func AgentConfigScanResultEvent(e v1.AgentConfigScanResultEvent) error {
	if err := AgentConfigScanAuthority(e.AgentConfigScanAuthority); err != nil {
		return err
	}
	if e.Kind != v1.EventAgentConfigScanResult || e.CommandMessageID != v1.AgentConfigScanCommandMessageID(e.RequestID) {
		return fmt.Errorf("scan result identity is invalid")
	}
	observed, err := AgentConfigScanTimestamp(e.ObservedAt)
	if err != nil {
		return err
	}
	deadline, _ := AgentConfigScanTimestamp(e.LeaseExpiresAt)
	if observed.After(deadline) {
		return fmt.Errorf("scan result is after its deadline")
	}
	switch e.Outcome.Kind {
	case "discovered":
		scan := e.Outcome.Scan
		if scan == nil || e.Outcome.Code != "" {
			return fmt.Errorf("discovered scan outcome is invalid")
		}
		if err := AgentConfigDiscoveryEvent(*scan); err != nil {
			return err
		}
		if scan.ProjectID != e.ProjectID || scan.RepositoryID != e.RepositoryID || scan.BranchRef != e.BranchRef || scan.ObservedAt.After(observed) {
			return fmt.Errorf("scan result scope or time mismatch")
		}
	case "failed":
		if e.Outcome.Scan != nil {
			return fmt.Errorf("failed scan cannot carry inventory")
		}
		switch e.Outcome.Code {
		case "repository_unavailable", "checkout_failed", "scan_failed", "deadline_exceeded", "interrupted", "stopped", "limit_exceeded", "repository_credential_unavailable", "repository_access_denied":
		default:
			return fmt.Errorf("unsupported scan failure code")
		}
	default:
		return fmt.Errorf("unsupported scan outcome")
	}
	return nil
}
