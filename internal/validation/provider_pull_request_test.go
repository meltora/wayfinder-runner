package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func providerPullRequestAuthorityForTest(operation string) v1.ProviderPullRequestAuthority {
	id := "22345678-1234-4234-8234-123456789abc"
	a := v1.ProviderPullRequestAuthority{AuthorityVersion: v1.ProviderPullRequestTransportVersion, OperationID: id, Operation: operation, OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1", RepositoryID: "repository-1", Provider: "github", InstallationID: "installation-1", ProviderInstanceKey: "github.com", ProviderRepositoryID: "1385038985", PullRequestNumber: "2", ExpectedHeadSHA: strings.Repeat("e", 40), LeaseID: v1.ProviderPullRequestLeaseID(id), FencingToken: 1, LeaseExpiresAt: time.Date(2026, 10, 12, 12, 5, 0, 0, time.UTC)}
	if operation == v1.ProviderPullRequestOperationMerge {
		method := "squash"
		a.MergeMethod = &method
	}
	a.CommandDigest = v1.ProviderPullRequestCommandDigest(a)
	return a
}

func TestProviderPullRequestCommandBindsOperationToCommandName(t *testing.T) {
	merge := providerPullRequestAuthorityForTest(v1.ProviderPullRequestOperationMerge)
	observe := providerPullRequestAuthorityForTest(v1.ProviderPullRequestOperationObserve)
	if err := ProviderPullRequestCommand(v1.CmdProviderPullRequestMerge, merge); err != nil {
		t.Fatal(err)
	}
	if err := ProviderPullRequestCommand(v1.CmdProviderPullRequestObservation, observe); err != nil {
		t.Fatal(err)
	}
	if ProviderPullRequestCommand(v1.CmdProviderPullRequestObservation, merge) == nil || ProviderPullRequestCommand(v1.CmdProviderPullRequestMerge, observe) == nil {
		t.Fatal("operation and command name drift was accepted")
	}
}

func TestProviderPullRequestAuthorityRejectsTampering(t *testing.T) {
	for name, mutate := range map[string]func(*v1.ProviderPullRequestAuthority){
		"digest":       func(a *v1.ProviderPullRequestAuthority) { a.ExpectedHeadSHA = strings.Repeat("f", 40) },
		"lease":        func(a *v1.ProviderPullRequestAuthority) { a.LeaseID = "lease-1" },
		"method":       func(a *v1.ProviderPullRequestAuthority) { a.MergeMethod = nil },
		"bad method":   func(a *v1.ProviderPullRequestAuthority) { m := "fast-forward"; a.MergeMethod = &m },
		"short head":   func(a *v1.ProviderPullRequestAuthority) { a.ExpectedHeadSHA = "e31aeb2" },
		"provider":     func(a *v1.ProviderPullRequestAuthority) { a.Provider = "gitlab" },
		"pull request": func(a *v1.ProviderPullRequestAuthority) { a.PullRequestNumber = "02" },
		"version":      func(a *v1.ProviderPullRequestAuthority) { a.AuthorityVersion = "provider-pull-request-v2" },
	} {
		a := providerPullRequestAuthorityForTest(v1.ProviderPullRequestOperationMerge)
		mutate(&a)
		if name != "digest" {
			a.CommandDigest = v1.ProviderPullRequestCommandDigest(a)
		}
		if ProviderPullRequestCommand(v1.CmdProviderPullRequestMerge, a) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestProviderPullRequestResultEventRequiresExactIdentityAndOutcome(t *testing.T) {
	a := providerPullRequestAuthorityForTest(v1.ProviderPullRequestOperationObserve)
	sha := strings.Repeat("a", 40)
	event := v1.ProviderPullRequestResultEvent{Kind: v1.EventProviderPullRequestResult, ProviderPullRequestAuthority: a, CommandMessageID: v1.ProviderPullRequestCommandMessageID(a.OperationID), ObservedAt: a.LeaseExpiresAt.Add(-time.Minute), Outcome: v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeObserved, State: "merged", HeadSHA: a.ExpectedHeadSHA, MergeCommitSHA: &sha, AllowedMergeMethods: []string{"squash"}}}
	if err := ProviderPullRequestResultEvent(event); err != nil {
		t.Fatal(err)
	}
	merged := event
	merged.Outcome = v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeMerged, MergeCommitSHA: &sha}
	if ProviderPullRequestResultEvent(merged) == nil {
		t.Fatal("observation reported a merge")
	}
	for name, mutate := range map[string]func(*v1.ProviderPullRequestResultEvent){
		"message":  func(e *v1.ProviderPullRequestResultEvent) { e.CommandMessageID = "provider-pull-request-command-x" },
		"late":     func(e *v1.ProviderPullRequestResultEvent) { e.ObservedAt = e.LeaseExpiresAt.Add(time.Second) },
		"fraction": func(e *v1.ProviderPullRequestResultEvent) { e.ObservedAt = e.ObservedAt.Add(time.Millisecond) },
		"state":    func(e *v1.ProviderPullRequestResultEvent) { e.Outcome.State = "draft" },
		"duplicate": func(e *v1.ProviderPullRequestResultEvent) {
			e.Outcome.AllowedMergeMethods = []string{"squash", "squash"}
		},
		"code": func(e *v1.ProviderPullRequestResultEvent) {
			e.Outcome = v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeFailed, Code: "boom"}
		},
	} {
		changed := event
		mutate(&changed)
		if ProviderPullRequestResultEvent(changed) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
