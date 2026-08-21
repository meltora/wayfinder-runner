package validation

import (
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validReviewerIdentityVerificationOperation() v1.ReviewerIdentityVerificationOperation {
	repositories := []v1.ReviewerIdentityVerificationRepository{
		{RepositoryID: "repository-1", RepositoryExternalID: "101"},
		{RepositoryID: "repository-2", RepositoryExternalID: "102"},
	}
	return v1.ReviewerIdentityVerificationOperation{
		SchemaVersion:                           v1.ReviewerIdentityVerificationSchemaVersion,
		ReviewerIdentityVerificationOperationID: "reviewer-identity-verification-operation-" + strings.Repeat("f", 64),
		OrganizationID:                          "organization-1", InstallationID: "installation-1",
		InstallationStateRevision: 2, InstallationAuthorityRevision: 3,
		InstallationAuthorityDigest: strings.Repeat("a", 64),
		Provider:                    "github", ProviderInstanceKey: "github.com",
		ProviderAccountProofID: "account-proof-1", ProviderAccountProofRevision: 4,
		ProviderAccountProofDigest: strings.Repeat("b", 64), ProviderAccountID: "42",
		RepositorySetDigest: v1.ReviewerIdentityRepositorySetDigest(repositories),
		OperationDigest:     strings.Repeat("c", 64), CommandDigest: strings.Repeat("d", 64),
		LeaseID: "identity-lease-1", FencingToken: 5,
		LeaseExpiresAt: time.Date(2030, 8, 21, 12, 0, 0, 0, time.UTC),
		Repositories:   repositories,
	}
}

func TestReviewerIdentityVerificationOperationRequiresExactBoundedRepositorySet(t *testing.T) {
	if err := ReviewerIdentityVerificationOperation(validReviewerIdentityVerificationOperation()); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*v1.ReviewerIdentityVerificationOperation){
		"schema": func(value *v1.ReviewerIdentityVerificationOperation) { value.SchemaVersion = 2 },
		"operation": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.ReviewerIdentityVerificationOperationID = "bad/id"
		},
		"organization":       func(value *v1.ReviewerIdentityVerificationOperation) { value.OrganizationID = "" },
		"installation":       func(value *v1.ReviewerIdentityVerificationOperation) { value.InstallationID = "" },
		"authority revision": func(value *v1.ReviewerIdentityVerificationOperation) { value.InstallationAuthorityRevision = 0 },
		"authority digest": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.InstallationAuthorityDigest = strings.Repeat("A", 64)
		},
		"provider": func(value *v1.ReviewerIdentityVerificationOperation) { value.Provider = "GitHub" },
		"provider instance": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.ProviderInstanceKey = "https://github.com"
		},
		"proof":                func(value *v1.ReviewerIdentityVerificationOperation) { value.ProviderAccountProofID = "" },
		"proof revision":       func(value *v1.ReviewerIdentityVerificationOperation) { value.ProviderAccountProofRevision = 0 },
		"proof digest":         func(value *v1.ReviewerIdentityVerificationOperation) { value.ProviderAccountProofDigest = "bad" },
		"provider account":     func(value *v1.ReviewerIdentityVerificationOperation) { value.ProviderAccountID = " octocat " },
		"empty repository set": func(value *v1.ReviewerIdentityVerificationOperation) { value.Repositories = nil },
		"unsorted repository set": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.Repositories[0], value.Repositories[1] = value.Repositories[1], value.Repositories[0]
		},
		"duplicate internal repository": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.Repositories[1].RepositoryID = "repository-1"
		},
		"duplicate provider repository": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.Repositories[1].RepositoryExternalID = "101"
		},
		"path used as github stable ID": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.Repositories[0].RepositoryExternalID = "octo/example"
		},
		"github path locator": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.Repositories[0].RepositoryPath = "octo/example"
		},
		"repository set over bound": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.Repositories = make([]v1.ReviewerIdentityVerificationRepository, maxReviewerIdentityRepositories+1)
			for index := range value.Repositories {
				value.Repositories[index] = v1.ReviewerIdentityVerificationRepository{
					RepositoryID:         fmt.Sprintf("repository-%03d", index),
					RepositoryExternalID: fmt.Sprintf("%d", index+1),
				}
			}
		},
		"non-numeric github account":  func(value *v1.ReviewerIdentityVerificationOperation) { value.ProviderAccountID = "octocat" },
		"noncanonical github account": func(value *v1.ReviewerIdentityVerificationOperation) { value.ProviderAccountID = "042" },
		"bad repository digest": func(value *v1.ReviewerIdentityVerificationOperation) {
			value.RepositorySetDigest = strings.Repeat("e", 64)
		},
		"bad operation digest": func(value *v1.ReviewerIdentityVerificationOperation) { value.OperationDigest = "bad" },
		"bad command digest":   func(value *v1.ReviewerIdentityVerificationOperation) { value.CommandDigest = "bad" },
		"lease":                func(value *v1.ReviewerIdentityVerificationOperation) { value.LeaseID = "" },
		"fence":                func(value *v1.ReviewerIdentityVerificationOperation) { value.FencingToken = 0 },
		"expiry":               func(value *v1.ReviewerIdentityVerificationOperation) { value.LeaseExpiresAt = time.Time{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validReviewerIdentityVerificationOperation()
			mutate(&candidate)
			if err := ReviewerIdentityVerificationOperation(candidate); err == nil {
				t.Fatal("invalid reviewer identity verification operation accepted")
			}
		})
	}
}

func TestReviewerIdentityVerificationAcceptsCanonicalProviderAccountIDBeyondJSONSafeInteger(t *testing.T) {
	operation := validReviewerIdentityVerificationOperation()
	operation.ProviderAccountID = "9007199254740993"
	if err := ReviewerIdentityVerificationOperation(operation); err != nil {
		t.Fatalf("canonical string account ID was truncated or rejected: %v", err)
	}
}

func TestReviewerIdentityVerificationAcceptsCanonicalBitbucketUUID(t *testing.T) {
	operation := validReviewerIdentityVerificationOperation()
	operation.Provider = "bitbucket"
	operation.ProviderInstanceKey = "bitbucket.org"
	operation.ProviderAccountID = "{504c3b62-8120-4f0c-a7bc-87800b9d6f70}"
	operation.Repositories = []v1.ReviewerIdentityVerificationRepository{
		{
			RepositoryID:         "repository-1",
			RepositoryExternalID: "{85d08b4e-571d-44e9-a507-fa476535aa98}",
			RepositoryPath:       "octo/example",
		},
	}
	operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
	if err := ReviewerIdentityVerificationOperation(operation); err != nil {
		t.Fatalf("canonical Bitbucket UUID was rejected: %v", err)
	}
}

func TestReviewerIdentityVerificationRejectsAmbiguousBitbucketRepositoryLocators(t *testing.T) {
	for name, mutate := range map[string]func(*v1.ReviewerIdentityVerificationOperation){
		"path as stable ID": func(operation *v1.ReviewerIdentityVerificationOperation) {
			operation.Repositories[0].RepositoryExternalID = "octo/example"
		},
		"missing path": func(operation *v1.ReviewerIdentityVerificationOperation) {
			operation.Repositories[0].RepositoryPath = ""
		},
		"noncanonical path": func(operation *v1.ReviewerIdentityVerificationOperation) {
			operation.Repositories[0].RepositoryPath = "Octo/example"
		},
		"duplicate path": func(operation *v1.ReviewerIdentityVerificationOperation) {
			operation.Repositories = append(operation.Repositories, v1.ReviewerIdentityVerificationRepository{
				RepositoryID:         "repository-2",
				RepositoryExternalID: "{123e4567-e89b-42d3-a456-426614174000}",
				RepositoryPath:       operation.Repositories[0].RepositoryPath,
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			operation := validReviewerIdentityVerificationOperation()
			operation.Provider = "bitbucket"
			operation.ProviderInstanceKey = "bitbucket.org"
			operation.ProviderAccountID = "557058:account-42"
			operation.Repositories = []v1.ReviewerIdentityVerificationRepository{{
				RepositoryID:         "repository-1",
				RepositoryExternalID: "{85d08b4e-571d-44e9-a507-fa476535aa98}",
				RepositoryPath:       "octo/example",
			}}
			mutate(&operation)
			operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
			if err := ReviewerIdentityVerificationOperation(operation); err == nil {
				t.Fatal("ambiguous Bitbucket repository locator accepted")
			}
		})
	}
}

func validReviewerIdentityVerificationResultEvent() v1.ReviewerIdentityVerificationResultEvent {
	operation := validReviewerIdentityVerificationOperation()
	return v1.ReviewerIdentityVerificationResultEvent{
		Kind: v1.EventReviewerIdentityVerificationResult,
		ReviewerIdentityVerificationResultAuthority: v1.ReviewerIdentityVerificationResultAuthorityFromOperation(operation),
		CommandMessageID: "reviewer-identity-verification-command-" + strings.Repeat("f", 64), ObservedAt: time.Date(2030, 8, 21, 11, 59, 0, 0, time.UTC),
		Outcome: v1.ReviewerIdentityVerificationResultOutcome{
			Kind:             v1.ReviewerIdentityVerificationSucceeded,
			ProviderUsername: "octocat", AssignmentPrincipal: "octocat",
			Repositories: []v1.ReviewerIdentityRepositoryEvidence{
				{RepositoryID: "repository-1", RepositoryExternalID: "101", EffectiveReviewerAccess: v1.ReviewerIdentityEligible},
				{RepositoryID: "repository-2", RepositoryExternalID: "102", EffectiveReviewerAccess: v1.ReviewerIdentityInsufficientPermission},
			},
		},
	}
}

func TestReviewerIdentityVerificationResultAllowsOnlyNormalizedEvidence(t *testing.T) {
	if err := ReviewerIdentityVerificationResultEvent(validReviewerIdentityVerificationResultEvent()); err != nil {
		t.Fatal(err)
	}
	retryAfter := uint64(30_000)
	retryable := validReviewerIdentityVerificationResultEvent()
	retryable.Outcome = v1.ReviewerIdentityVerificationResultOutcome{
		Kind:         v1.ReviewerIdentityVerificationRetryable,
		Code:         v1.ReviewerIdentityProviderUnavailable,
		RetryAfterMS: &retryAfter,
	}
	if err := ReviewerIdentityVerificationResultEvent(retryable); err != nil {
		t.Fatal(err)
	}
	permanent := validReviewerIdentityVerificationResultEvent()
	permanent.Outcome = v1.ReviewerIdentityVerificationResultOutcome{
		Kind: v1.ReviewerIdentityVerificationPermanent,
		Code: v1.ReviewerIdentityAccountNotFound,
	}
	if err := ReviewerIdentityVerificationResultEvent(permanent); err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*v1.ReviewerIdentityVerificationResultEvent){
		"kind":     func(value *v1.ReviewerIdentityVerificationResultEvent) { value.Kind = "identity_result" },
		"command":  func(value *v1.ReviewerIdentityVerificationResultEvent) { value.CommandMessageID = "bad/id" },
		"observed": func(value *v1.ReviewerIdentityVerificationResultEvent) { value.ObservedAt = time.Time{} },
		"observed after expiry": func(value *v1.ReviewerIdentityVerificationResultEvent) {
			value.ObservedAt = value.LeaseExpiresAt.Add(time.Second)
		},
		"success without username": func(value *v1.ReviewerIdentityVerificationResultEvent) { value.Outcome.ProviderUsername = "" },
		"success with code": func(value *v1.ReviewerIdentityVerificationResultEvent) {
			value.Outcome.Code = v1.ReviewerIdentityInstallationInactive
		},
		"username control": func(value *v1.ReviewerIdentityVerificationResultEvent) { value.Outcome.ProviderUsername = "octo\ncat" },
		"empty principal":  func(value *v1.ReviewerIdentityVerificationResultEvent) { value.Outcome.AssignmentPrincipal = "" },
		"unsupported access": func(value *v1.ReviewerIdentityVerificationResultEvent) {
			value.Outcome.Repositories[0].EffectiveReviewerAccess = "guest"
		},
		"retryable permanent code": func(value *v1.ReviewerIdentityVerificationResultEvent) {
			value.Outcome = v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationRetryable, Code: v1.ReviewerIdentityInstallationInactive}
		},
		"permanent evidence": func(value *v1.ReviewerIdentityVerificationResultEvent) {
			value.Outcome.Kind = v1.ReviewerIdentityVerificationPermanent
			value.Outcome.Code = v1.ReviewerIdentityInstallationInactive
		},
		"raw error": func(value *v1.ReviewerIdentityVerificationResultEvent) {
			value.Outcome = v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationPermanent, Code: "token=secret"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validReviewerIdentityVerificationResultEvent()
			mutate(&candidate)
			if err := ReviewerIdentityVerificationResultEvent(candidate); err == nil {
				t.Fatal("invalid reviewer identity verification result accepted")
			}
		})
	}
}
