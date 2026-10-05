package enroll

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"
)

const (
	rejectedMessage    = "enrollment code was not accepted: it may be mistyped, expired, already used or revoked; create a new code in the dashboard and run enroll again"
	malformedMessage   = "that is not an enrollment code: copy the whole code from the dashboard (it starts with ren1.)"
	invalidMessage     = "the control plane refused this runner's enrollment request (status 422); check that this runner version is supported by the control plane"
	unavailableMessage = "runner enrollment is not available on this control plane right now; try again later, and check --url if it keeps failing"
)

func rejectingServer(t *testing.T, status int, body string, header map[string]string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, value := range header {
			w.Header().Set(name, value)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, AllowInsecure: true}
}

func enrollError(t *testing.T, client *Client) error {
	t.Helper()
	_, err := client.Enroll(context.Background(), Request{EnrollmentCode: "ren1.secret-code", CSR: "csr"})
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

func TestEnrollRejectionMessages(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		header map[string]string
		code   string
		want   string
	}{
		"rejected":             {403, `{"code":"enrollment_code_rejected","error":"Runner enrollment could not be completed"}`, nil, "enrollment_code_rejected", rejectedMessage},
		"malformed":            {422, `{"code":"enrollment_code_malformed","error":"x"}`, nil, "enrollment_code_malformed", malformedMessage},
		"invalid":              {422, `{"code":"enrollment_request_invalid","error":"x"}`, nil, "enrollment_request_invalid", invalidMessage},
		"rate limited":         {429, `{"code":"enrollment_rate_limited","error":"x"}`, map[string]string{"Retry-After": "45"}, "enrollment_rate_limited", "too many enrollment attempts from this address; wait 45 seconds and run enroll again"},
		"rate limited no wait": {429, `{"code":"enrollment_rate_limited","error":"x"}`, map[string]string{"Retry-After": "soon"}, "enrollment_rate_limited", "too many enrollment attempts from this address; wait a minute and run enroll again"},
		"unavailable":          {503, `{"code":"enrollment_unavailable","error":"x"}`, nil, "enrollment_unavailable", unavailableMessage},
		"expired":              {403, `{"code":"enrollment_code_expired","error":"x"}`, nil, "enrollment_code_expired", "this enrollment code has expired; create a new code in the dashboard and run enroll again"},
		"used":                 {403, `{"code":"enrollment_code_used","error":"x"}`, nil, "enrollment_code_used", "this enrollment code was already used by another runner; create a new code in the dashboard and run enroll again"},
		"revoked":              {403, `{"code":"enrollment_code_revoked","error":"x"}`, nil, "enrollment_code_revoked", "this enrollment code was revoked in the dashboard; create a new code and run enroll again"},
		"legacy denied":        {403, `{"code":"RUNNER_ENROLLMENT_DENIED","error":"x"}`, nil, "RUNNER_ENROLLMENT_DENIED", rejectedMessage},
		"legacy validation":    {422, `{"code":"VALIDATION","error":"x"}`, nil, "VALIDATION", invalidMessage},
		"legacy throttled":     {429, `{"code":"TOO_MANY_REQUESTS","error":"x"}`, nil, "TOO_MANY_REQUESTS", "too many enrollment attempts from this address; wait a minute and run enroll again"},
		"legacy unavailable":   {503, `{"code":"RUNNER_ENROLLMENT_UNAVAILABLE","error":"x"}`, nil, "RUNNER_ENROLLMENT_UNAVAILABLE", unavailableMessage},
		"non-JSON body":        {404, `<html>not found</html>`, nil, "", "enroll rejected: status 404: <html>not found</html>"},
		"unknown code":         {400, `{"code":"something_new","error":"x"}`, nil, "something_new", `enroll rejected: status 400: {"code":"something_new","error":"x"}`},
		"control characters":   {500, "\x1b[31mboom\r\nnext\x07", nil, "", "enroll rejected: status 500: [31mboomnext"},
		"unsafe code":          {400, `{"code":"bad\u001b[2Jcode","error":"x"}`, nil, "", `enroll rejected: status 400: {"code":"bad\u001b[2Jcode","error":"x"}`},
	} {
		t.Run(name, func(t *testing.T) {
			err := enrollError(t, rejectingServer(t, tc.status, tc.body, tc.header))
			if err.Error() != tc.want {
				t.Fatalf("error = %q\nwant    %q", err.Error(), tc.want)
			}
			var rejected *RejectedError
			if !errors.As(err, &rejected) || rejected.Status != tc.status || rejected.Code != tc.code {
				t.Fatalf("rejection = %+v", rejected)
			}
			if strings.Contains(err.Error(), "secret-code") || strings.ContainsFunc(err.Error(), unicode.IsControl) {
				t.Fatalf("error text leaks: %q", err.Error())
			}
			if _, known := rejectionKinds[tc.code]; known && strings.Contains(err.Error(), "could not be completed") {
				t.Fatalf("error text repeats the server message: %q", err.Error())
			}
		})
	}
}
