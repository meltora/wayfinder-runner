package sourcepublication

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestGitLabAssignReviewersUnionsExistingBeforeReplacement(t *testing.T) {
	var replaced []uint64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /projects/123":
			_, _ = io.WriteString(response, `{"id":123,"path_with_namespace":"acme/repo"}`)
		case "GET /projects/123/merge_requests/7":
			reviewers := `[{"id":99,"username":"existing","state":"active"}]`
			if len(replaced) != 0 {
				reviewers = `[{"id":99,"username":"existing","state":"active"},{"id":42,"username":"alice","state":"active"}]`
			}
			_, _ = io.WriteString(response, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567","reviewers":`+reviewers+`}`)
		case "GET /users/42":
			_, _ = io.WriteString(response, `{"id":42,"username":"alice","state":"active"}`)
		case "GET /projects/123/members/all/42":
			_, _ = io.WriteString(response, `{"id":42,"username":"alice","state":"active","access_level":30}`)
		case "PUT /projects/123/merge_requests/7":
			var body struct {
				ReviewerIDs []uint64 `json:"reviewer_ids"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			replaced = body.ReviewerIDs
			_, _ = io.WriteString(response, `{}`)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	command := assignmentOperationForTest("gitlab")
	outcome := newGitLabPublisher(server.URL, "secret", server.Client()).assignReviewers(context.Background(), command)
	if outcome.Kind != v1.ProviderReviewerAssignmentSucceeded {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
	if !reflect.DeepEqual(replaced, []uint64{42, 99}) {
		t.Fatalf("reviewer_ids = %#v", replaced)
	}
}

func TestGitLabAssignReviewersRejectsIdentityDriftBeforeMutation(t *testing.T) {
	putCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /projects/123":
			_, _ = io.WriteString(response, `{"id":123,"path_with_namespace":"acme/repo"}`)
		case "GET /projects/123/merge_requests/7":
			_, _ = io.WriteString(response, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567","reviewers":[]}`)
		case "GET /users/42":
			_, _ = io.WriteString(response, `{"id":42,"username":"renamed","state":"active"}`)
		case "GET /projects/123/members/all/42":
			_, _ = io.WriteString(response, `{"id":42,"username":"renamed","state":"active","access_level":30}`)
		case "PUT /projects/123/merge_requests/7":
			putCount++
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	command := assignmentOperationForTest("gitlab")
	outcome := newGitLabPublisher(server.URL, "secret", server.Client()).assignReviewers(context.Background(), command)
	if outcome.Kind != v1.ProviderReviewerAssignmentPermanent ||
		outcome.Code != v1.ProviderReviewerAssignmentReviewerIdentityChanged || putCount != 0 {
		t.Fatalf("outcome=%#v putCount=%d", outcome, putCount)
	}
}
