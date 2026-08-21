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

func TestBitbucketAssignReviewersUnionsExistingBeforeReplacement(t *testing.T) {
	existingUUID := "{99999999-9999-4999-8999-999999999999}"
	desiredUUID := "{11111111-1111-4111-8111-111111111111}"
	var replaced []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /repositories/acme/repo":
			_, _ = io.WriteString(response, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
		case "GET /repositories/acme/repo/pullrequests/7":
			reviewers := `[{"account_id":"existing-account","uuid":"` + existingUUID + `","nickname":"existing","account_status":"active"}]`
			if len(replaced) != 0 {
				reviewers = `[{"account_id":"existing-account","uuid":"` + existingUUID + `","nickname":"existing","account_status":"active"},{"account_id":"bb-account-42","uuid":"` + desiredUUID + `","nickname":"alice","account_status":"active"}]`
			}
			_, _ = io.WriteString(response, `{"id":7,"state":"OPEN","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":`+reviewers+`}`)
		case "GET /users/bb-account-42":
			_, _ = io.WriteString(response, `{"account_id":"bb-account-42","uuid":"`+desiredUUID+`","nickname":"alice","account_status":"active"}`)
		case "GET /workspaces/acme/permissions/repositories/repo":
			_, _ = io.WriteString(response, `{"page":1,"pagelen":100,"size":1,"values":[{"permission":"read","user":{"account_id":"bb-account-42","uuid":"`+desiredUUID+`"},"repository":{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}}]}`)
		case "PUT /repositories/acme/repo/pullrequests/7":
			var body struct {
				Reviewers []struct {
					UUID string `json:"uuid"`
				} `json:"reviewers"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			for _, reviewer := range body.Reviewers {
				replaced = append(replaced, reviewer.UUID)
			}
			_, _ = io.WriteString(response, `{}`)
		default:
			t.Errorf("unexpected request: %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	command := assignmentOperationForTest("bitbucket")
	outcome := newBitbucketPublisher(server.URL, "secret", server.Client()).assignReviewers(context.Background(), command)
	if outcome.Kind != v1.ProviderReviewerAssignmentSucceeded {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
	if !reflect.DeepEqual(replaced, []string{desiredUUID, existingUUID}) {
		t.Fatalf("reviewers = %#v", replaced)
	}
}

func TestBitbucketAssignReviewersRejectsPartialPostMutationStateAsUncertain(t *testing.T) {
	desiredUUID := "{11111111-1111-4111-8111-111111111111}"
	operation := assignmentOperationForTest("bitbucket")
	second := operation.Recipients[0]
	second.ReviewerIdentityID = "identity-2"
	second.UserID = "user-2"
	second.ProviderAccountID = "bb-account-43"
	second.ProviderUsername = "bob"
	second.AssignmentPrincipal = "{33333333-3333-4333-8333-333333333333}"
	operation.Recipients = append(operation.Recipients, second)
	mutated := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /repositories/acme/repo":
			_, _ = io.WriteString(response, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
		case "GET /repositories/acme/repo/pullrequests/7":
			reviewers := `[]`
			if mutated {
				reviewers = `[{"account_id":"bb-account-42","uuid":"` + desiredUUID + `","nickname":"alice","account_status":"active"}]`
			}
			_, _ = io.WriteString(response, `{"id":7,"state":"OPEN","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":`+reviewers+`}`)
		case "GET /users/bb-account-42":
			_, _ = io.WriteString(response, `{"account_id":"bb-account-42","uuid":"`+desiredUUID+`","nickname":"alice","account_status":"active"}`)
		case "GET /users/bb-account-43":
			_, _ = io.WriteString(response, `{"account_id":"bb-account-43","uuid":"{33333333-3333-4333-8333-333333333333}","nickname":"bob","account_status":"active"}`)
		case "GET /workspaces/acme/permissions/repositories/repo":
			accountID := "bb-account-42"
			uuid := desiredUUID
			if request.URL.Query().Get("q") == `user.account_id = "bb-account-43"` {
				accountID = "bb-account-43"
				uuid = "{33333333-3333-4333-8333-333333333333}"
			}
			_, _ = io.WriteString(response, `{"page":1,"pagelen":100,"size":1,"values":[{"permission":"read","user":{"account_id":"`+accountID+`","uuid":"`+uuid+`"},"repository":{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}}]}`)
		case "PUT /repositories/acme/repo/pullrequests/7":
			mutated = true
			_, _ = io.WriteString(response, `{}`)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	outcome := newBitbucketPublisher(server.URL, "secret", server.Client()).assignReviewers(context.Background(), operation)
	if outcome.Kind != v1.ProviderReviewerAssignmentUncertain ||
		outcome.Code != v1.ProviderReviewerAssignmentDeliveryUncertain {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
}
