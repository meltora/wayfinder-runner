package sourcepublication

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubAssignmentSnapshotFailsClosedOnDriftAndMalformedEvidence(t *testing.T) {
	for _, mode := range []string{"closed", "head", "requested", "reviews"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/repos/acme/repo/pulls/7":
					state := "open"
					head := "0123456789abcdef0123456789abcdef01234567"
					if mode == "closed" {
						state = "closed"
					} else if mode == "head" {
						head = "ffffffffffffffffffffffffffffffffffffffff"
					}
					_, _ = io.WriteString(response, `{"number":7,"state":"`+state+`","head":{"sha":"`+head+`"}}`)
				case "/repos/acme/repo/pulls/7/requested_reviewers":
					if mode == "requested" {
						_, _ = io.WriteString(response, `{}`)
					} else {
						_, _ = io.WriteString(response, `{"users":[],"teams":[]}`)
					}
				case "/repos/acme/repo/pulls/7/reviews":
					_, _ = io.WriteString(response, `{}`)
				}
			}))
			defer server.Close()
			publisher := newGitHubPublisher(server.URL, "secret", server.Client())
			_, failure := publisher.assignmentSnapshot(
				context.Background(), "/repos/acme/repo/pulls/7", assignmentOperationForTest("github"),
			)
			if failure == nil {
				t.Fatal("hostile GitHub response accepted")
			}
		})
	}
}

func TestGitLabAssignmentSnapshotFailsClosedOnDriftAndMalformedEvidence(t *testing.T) {
	for _, mode := range []string{"closed", "head", "reviewers"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				state := "opened"
				head := "0123456789abcdef0123456789abcdef01234567"
				reviewers := `[]`
				if mode == "closed" {
					state = "closed"
				} else if mode == "head" {
					head = "ffffffffffffffffffffffffffffffffffffffff"
				} else if mode == "reviewers" {
					reviewers = `[{"id":0,"username":""}]`
				}
				_, _ = io.WriteString(response, `{"iid":7,"state":"`+state+`","sha":"`+head+`","reviewers":`+reviewers+`}`)
			}))
			defer server.Close()
			publisher := newGitLabPublisher(server.URL, "secret", server.Client())
			_, failure := publisher.assignmentSnapshot(
				context.Background(), "/projects/123/merge_requests/7", assignmentOperationForTest("gitlab"),
			)
			if failure == nil {
				t.Fatal("hostile GitLab response accepted")
			}
		})
	}
}

func TestBitbucketAssignmentSnapshotFailsClosedOnDriftAndMalformedEvidence(t *testing.T) {
	for _, mode := range []string{"closed", "head", "repository", "reviewers"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				state := "OPEN"
				head := "0123456789abcdef0123456789abcdef01234567"
				repository := "acme/repo"
				reviewers := `[]`
				if mode == "closed" {
					state = "DECLINED"
				} else if mode == "head" {
					head = "ffffffffffffffffffffffffffffffffffffffff"
				} else if mode == "repository" {
					repository = "attacker/repo"
				} else if mode == "reviewers" {
					reviewers = `[{"account_id":"x","uuid":"invalid","nickname":"x"}]`
				}
				_, _ = io.WriteString(response, `{"id":7,"state":"`+state+`","source":{"commit":{"hash":"`+head+`"},"repository":{"full_name":"`+repository+`"}},"destination":{"repository":{"full_name":"`+repository+`"}},"reviewers":`+reviewers+`}`)
			}))
			defer server.Close()
			publisher := newBitbucketPublisher(server.URL, "secret", server.Client())
			_, failure := publisher.assignmentSnapshot(
				context.Background(), "/repositories/acme/repo/pullrequests/7", assignmentOperationForTest("bitbucket"),
			)
			if failure == nil {
				t.Fatal("hostile Bitbucket response accepted")
			}
		})
	}
}
