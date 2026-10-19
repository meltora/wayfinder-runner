package validation

import (
	"fmt"
	"regexp"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var (
	providerPullRequestOperationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	providerPullRequestDecimalPattern     = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)
	providerPullRequestHeadPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	providerPullRequestMergeMethods       = map[string]struct{}{"merge": {}, "squash": {}, "rebase": {}}
	providerPullRequestFailureCodes       = map[v1.ProviderPullRequestErrorCode]struct{}{
		v1.ProviderPullRequestProviderUnavailable: {}, v1.ProviderPullRequestRateLimited: {}, v1.ProviderPullRequestPermissionDenied: {},
		v1.ProviderPullRequestInstallationInactive: {}, v1.ProviderPullRequestRepositoryMismatch: {}, v1.ProviderPullRequestNotFound: {},
		v1.ProviderPullRequestNotOpen: {}, v1.ProviderPullRequestHeadChanged: {}, v1.ProviderPullRequestNotMergeable: {},
		v1.ProviderPullRequestMergeMethodNotAllowed: {}, v1.ProviderPullRequestDeadlineExceeded: {},
	}
)

func canonicalProviderPullRequestTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(v1.ProviderPullRequestTime(value))
}

func providerPullRequestAuthority(v v1.ProviderPullRequestAuthority) error {
	if v.AuthorityVersion != v1.ProviderPullRequestTransportVersion || !providerPullRequestOperationIDPattern.MatchString(v.OperationID) {
		return fmt.Errorf("provider pull-request authority is invalid")
	}
	for field, id := range map[string]string{
		"organization_id": v.OrganizationID, "project_id": v.ProjectID, "workflow_run_id": v.WorkflowRunID,
		"repository_id": v.RepositoryID, "installation_id": v.InstallationID, "lease_id": v.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if v.Provider != "github" || v.ProviderInstanceKey == "" || !providerPullRequestDecimalPattern.MatchString(v.ProviderRepositoryID) || !providerPullRequestDecimalPattern.MatchString(v.PullRequestNumber) || !providerPullRequestHeadPattern.MatchString(v.ExpectedHeadSHA) {
		return fmt.Errorf("provider pull-request subject is invalid")
	}
	switch v.Operation {
	case v1.ProviderPullRequestOperationObserve:
		if v.MergeMethod != nil {
			return fmt.Errorf("provider pull-request observation must not carry a merge method")
		}
	case v1.ProviderPullRequestOperationMerge:
		if v.MergeMethod == nil {
			return fmt.Errorf("provider pull-request merge method is missing")
		}
		if _, ok := providerPullRequestMergeMethods[*v.MergeMethod]; !ok {
			return fmt.Errorf("provider pull-request merge method is invalid")
		}
	default:
		return fmt.Errorf("provider pull-request operation is invalid")
	}
	if v.LeaseID != v1.ProviderPullRequestLeaseID(v.OperationID) || v.FencingToken == 0 || v.FencingToken > maxJSONSafeInteger || !canonicalProviderPullRequestTime(v.LeaseExpiresAt) {
		return fmt.Errorf("provider pull-request lease is invalid")
	}
	if err := SHA256Digest("command_digest", v.CommandDigest); err != nil {
		return err
	}
	if v1.ProviderPullRequestCommandDigest(v) != v.CommandDigest {
		return fmt.Errorf("provider pull-request command digest differs")
	}
	return nil
}

func ProviderPullRequestCommand(command string, v v1.ProviderPullRequestAuthority) error {
	expected := map[string]string{v1.CmdProviderPullRequestObservation: v1.ProviderPullRequestOperationObserve, v1.CmdProviderPullRequestMerge: v1.ProviderPullRequestOperationMerge}[command]
	if expected == "" || v.Operation != expected {
		return fmt.Errorf("provider pull-request operation differs from command")
	}
	return providerPullRequestAuthority(v)
}

func ProviderPullRequestCommandMessageID(id string, v v1.ProviderPullRequestAuthority) error {
	if id != v1.ProviderPullRequestCommandMessageID(v.OperationID) {
		return fmt.Errorf("command message ID is invalid")
	}
	return nil
}

func ProviderPullRequestResultEvent(e v1.ProviderPullRequestResultEvent) error {
	if e.Kind != v1.EventProviderPullRequestResult {
		return fmt.Errorf("result kind is invalid")
	}
	if err := providerPullRequestAuthority(e.ProviderPullRequestAuthority); err != nil {
		return err
	}
	if err := ProviderPullRequestCommandMessageID(e.CommandMessageID, e.ProviderPullRequestAuthority); err != nil {
		return err
	}
	if !canonicalProviderPullRequestTime(e.ObservedAt) || e.ObservedAt.After(e.LeaseExpiresAt) {
		return fmt.Errorf("result observation time is invalid")
	}
	o := e.Outcome
	switch o.Kind {
	case v1.ProviderPullRequestOutcomeObserved:
		if (o.State != "open" && o.State != "closed" && o.State != "merged") || !providerPullRequestHeadPattern.MatchString(o.HeadSHA) || o.Code != "" {
			return fmt.Errorf("observed outcome is invalid")
		}
		if o.MergeCommitSHA != nil && !providerPullRequestHeadPattern.MatchString(*o.MergeCommitSHA) {
			return fmt.Errorf("observed merge commit is invalid")
		}
		if len(o.AllowedMergeMethods) > 3 {
			return fmt.Errorf("allowed merge methods are invalid")
		}
		seen := map[string]struct{}{}
		for _, method := range o.AllowedMergeMethods {
			if _, ok := providerPullRequestMergeMethods[method]; !ok {
				return fmt.Errorf("allowed merge methods are invalid")
			}
			if _, duplicate := seen[method]; duplicate {
				return fmt.Errorf("allowed merge methods are invalid")
			}
			seen[method] = struct{}{}
		}
	case v1.ProviderPullRequestOutcomeMerged:
		if e.Operation != v1.ProviderPullRequestOperationMerge || o.MergeCommitSHA == nil || !providerPullRequestHeadPattern.MatchString(*o.MergeCommitSHA) || o.Code != "" {
			return fmt.Errorf("merged outcome is invalid")
		}
	case v1.ProviderPullRequestOutcomeFailed:
		if _, ok := providerPullRequestFailureCodes[o.Code]; !ok {
			return fmt.Errorf("failure code is invalid")
		}
	default:
		return fmt.Errorf("outcome kind is invalid")
	}
	return nil
}
