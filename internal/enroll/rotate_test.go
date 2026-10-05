package enroll

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode"
)

func rotateError(t *testing.T, client *Client) error {
	t.Helper()
	_, err := client.Rotate(context.Background(), RotateRequest{CSR: "csr", RunnerVersion: "1.0.0"})
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

func TestRotateRejectionCarriesPublicCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status    int
		body      string
		code      string
		keyReused bool
		transient bool
	}{
		{http.StatusConflict, `{"code":"rotation_key_reused","error":"Runner certificate rotation could not be completed"}`, "rotation_key_reused", true, false},
		{http.StatusConflict, `{"code":"rotation_too_early","error":"x"}`, "rotation_too_early", false, false},
		{http.StatusBadRequest, `{"code":"rotation_request_invalid","error":"x"}`, "rotation_request_invalid", false, false},
		{http.StatusForbidden, `{"code":"runner_unavailable","error":"x"}`, "runner_unavailable", false, false},
		{http.StatusTooManyRequests, `{"code":"rotation_rate_limited","error":"x"}`, "rotation_rate_limited", false, false},
		{http.StatusServiceUnavailable, `{"code":"rotation_unavailable","error":"x"}`, "rotation_unavailable", false, true},
		{http.StatusUnauthorized, ``, "", false, false},
		{http.StatusNotFound, ``, "", false, false},
		{http.StatusBadGateway, `upstream`, "", false, true},
		{http.StatusConflict, `{"code":"not a safe code!","error":"x"}`, "", false, false},
	}
	for _, tc := range cases {
		err := rotateError(t, rejectingServer(t, tc.status, tc.body, nil))
		var rejected *RotateRejectedError
		if !errors.As(err, &rejected) {
			t.Fatalf("status %d: error %T is not a RotateRejectedError", tc.status, err)
		}
		if rejected.Status != tc.status || rejected.Code != tc.code {
			t.Fatalf("status %d: got status %d code %q, want code %q", tc.status, rejected.Status, rejected.Code, tc.code)
		}
		if rejected.KeyReused() != tc.keyReused || rejected.Transient() != tc.transient {
			t.Fatalf("status %d code %q: keyReused=%v transient=%v", tc.status, tc.code, rejected.KeyReused(), rejected.Transient())
		}
		if !strings.Contains(err.Error(), "rotate rejected: status") {
			t.Fatalf("status %d: message %q lost the rotate prefix", tc.status, err.Error())
		}
	}
}

func TestRotateRejectionStripsControlCharacters(t *testing.T) {
	t.Parallel()
	err := rotateError(t, rejectingServer(t, http.StatusConflict, "{\"code\":\"rotation_too_early\",\"error\":\"\x1b[31mred\r\nforged log line\x07\"}", nil))
	for _, r := range err.Error() {
		if unicode.IsControl(r) {
			t.Fatalf("rotation error contains control character %q: %q", r, err.Error())
		}
	}
	if !strings.Contains(err.Error(), "rotation_too_early") {
		t.Fatalf("rotation error %q lost its code", err.Error())
	}
}
