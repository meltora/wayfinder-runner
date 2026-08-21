package sourcepublication

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestGitHubAssignReviewersRevalidatesAndPreservesExisting(t *testing.T) {
	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /repositories/123":
			_, _ = io.WriteString(response, `{"id":123,"full_name":"acme/repo"}`)
		case "GET /repos/acme/repo/pulls/7":
			_, _ = io.WriteString(response, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "GET /repos/acme/repo/pulls/7/requested_reviewers":
			if len(posted) == 0 {
				_, _ = io.WriteString(response, `{"users":[{"id":99,"login":"existing","type":"User"}],"teams":[]}`)
			} else {
				_, _ = io.WriteString(response, `{"users":[{"id":99,"login":"existing","type":"User"},{"id":42,"login":"alice","type":"User"}],"teams":[]}`)
			}
		case "GET /repos/acme/repo/pulls/7/reviews":
			_, _ = io.WriteString(response, `[]`)
		case "GET /user/42":
			_, _ = io.WriteString(response, `{"id":42,"login":"alice","type":"User"}`)
		case "GET /repos/acme/repo/collaborators/alice/permission":
			_, _ = io.WriteString(response, `{"permission":"read","role_name":"read","user":{"id":42,"login":"alice","type":"User"}}`)
		case "POST /repos/acme/repo/pulls/7/requested_reviewers":
			var body struct {
				Reviewers []string `json:"reviewers"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			posted = body.Reviewers
			_, _ = io.WriteString(response, `{}`)
		default:
			t.Errorf("unexpected request: %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()

	command := assignmentOperationForTest("github")
	outcome := newGitHubPublisher(server.URL, "secret", server.Client()).assignReviewers(context.Background(), command)
	if outcome.Kind != v1.ProviderReviewerAssignmentSucceeded {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
	if !reflect.DeepEqual(posted, []string{"alice"}) {
		t.Fatalf("posted reviewers = %#v", posted)
	}
}

func TestGitHubAssignReviewersCountsSubmittedReviewsDuringReconciliation(t *testing.T) {
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /repositories/123":
			_, _ = io.WriteString(response, `{"id":123,"full_name":"acme/repo"}`)
		case "GET /repos/acme/repo/pulls/7":
			_, _ = io.WriteString(response, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "GET /repos/acme/repo/pulls/7/requested_reviewers":
			_, _ = io.WriteString(response, `{"users":[],"teams":[]}`)
		case "GET /repos/acme/repo/pulls/7/reviews":
			_, _ = io.WriteString(response, `[{"id":1,"state":"APPROVED","user":{"id":42,"login":"alice","type":"User"}}]`)
		case "GET /user/42":
			_, _ = io.WriteString(response, `{"id":42,"login":"alice","type":"User"}`)
		case "GET /repos/acme/repo/collaborators/alice/permission":
			_, _ = io.WriteString(response, `{"permission":"read","user":{"id":42,"login":"alice","type":"User"}}`)
		case "POST /repos/acme/repo/pulls/7/requested_reviewers":
			postCount++
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	command := assignmentOperationForTest("github")
	outcome := newGitHubPublisher(server.URL, "secret", server.Client()).assignReviewers(context.Background(), command)
	if outcome.Kind != v1.ProviderReviewerAssignmentSucceeded || postCount != 0 {
		t.Fatalf("outcome=%#v postCount=%d", outcome, postCount)
	}
}

func TestGitHubAssignReviewersPaginatesSubmittedReviewsByStableAccountID(t *testing.T) {
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /repositories/123":
			_, _ = io.WriteString(response, `{"id":123,"full_name":"acme/repo"}`)
		case "GET /repos/acme/repo/pulls/7":
			_, _ = io.WriteString(response, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "GET /repos/acme/repo/pulls/7/requested_reviewers":
			_, _ = io.WriteString(response, `{"users":[],"teams":[]}`)
		case "GET /repos/acme/repo/pulls/7/reviews":
			if request.URL.Query().Get("page") == "1" {
				reviews := make([]map[string]any, 100)
				for index := range reviews {
					reviews[index] = map[string]any{
						"id": index + 1, "state": "COMMENTED",
						"user": map[string]any{"id": index + 1000, "login": "other", "type": "User"},
					}
				}
				_ = json.NewEncoder(response).Encode(reviews)
			} else {
				_, _ = io.WriteString(response, `[{"id":101,"state":"APPROVED","user":{"id":42,"login":"alice","type":"User"}}]`)
			}
		case "GET /user/42":
			_, _ = io.WriteString(response, `{"id":42,"login":"alice","type":"User"}`)
		case "GET /repos/acme/repo/collaborators/alice/permission":
			_, _ = io.WriteString(response, `{"permission":"read","user":{"id":42,"login":"alice","type":"User"}}`)
		case "POST /repos/acme/repo/pulls/7/requested_reviewers":
			postCount++
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	outcome := newGitHubPublisher(server.URL, "secret", server.Client()).assignReviewers(
		context.Background(), assignmentOperationForTest("github"),
	)
	if outcome.Kind != v1.ProviderReviewerAssignmentSucceeded || postCount != 0 {
		t.Fatalf("outcome=%#v postCount=%d", outcome, postCount)
	}
}

func TestGitHubAssignReviewersReturnsUncertainAfterMutationResponseLoss(t *testing.T) {
	// The transport returns EOF for the mutation and the reconciliation read
	// cannot prove that the requested reviewer is present. Retrying would risk
	// another unobserved side effect, so the outcome must be terminal/uncertain.
	roundTripper := &scriptedGitHubResponseLoss{}
	client := &http.Client{Transport: roundTripper}
	command := assignmentOperationForTest("github")
	outcome := newGitHubPublisher("https://api.github.test", "secret", client).assignReviewers(context.Background(), command)
	if outcome.Kind != v1.ProviderReviewerAssignmentUncertain ||
		outcome.Code != v1.ProviderReviewerAssignmentDeliveryUncertain {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
}

type scriptedGitHubResponseLoss struct{ requestedReads int }

func (script *scriptedGitHubResponseLoss) RoundTrip(request *http.Request) (*http.Response, error) {
	status := http.StatusOK
	body := `{}`
	switch request.Method + " " + request.URL.Path {
	case "GET /repositories/123":
		body = `{"id":123,"full_name":"acme/repo"}`
	case "GET /repos/acme/repo/pulls/7":
		body = `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`
	case "GET /repos/acme/repo/pulls/7/requested_reviewers":
		script.requestedReads++
		body = `{"users":[],"teams":[]}`
	case "GET /repos/acme/repo/pulls/7/reviews":
		body = `[]`
	case "GET /user/42":
		body = `{"id":42,"login":"alice","type":"User"}`
	case "GET /repos/acme/repo/collaborators/alice/permission":
		body = `{"permission":"read","user":{"id":42,"login":"alice","type":"User"}}`
	case "POST /repos/acme/repo/pulls/7/requested_reviewers":
		return nil, io.ErrUnexpectedEOF
	default:
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}
