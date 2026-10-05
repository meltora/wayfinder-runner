package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollPrintsSpecificRejection(t *testing.T) {
	for code, want := range map[string]string{
		"enrollment_code_rejected": "enrollment code was not accepted: it may be mistyped, expired, already used or revoked; create a new code in the dashboard and run enroll again",
		"enrollment_code_expired":  "this enrollment code has expired; create a new code in the dashboard and run enroll again",
		"enrollment_code_used":     "this enrollment code was already used by another runner; create a new code in the dashboard and run enroll again",
		"enrollment_code_revoked":  "this enrollment code was revoked in the dashboard; create a new code and run enroll again",
	} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"` + code + `","error":"Runner enrollment could not be completed"}`))
			}))
			t.Cleanup(server.Close)
			withDefaultTransport(t, server.Client().Transport)
			withStampedRelease(t, "", "")
			err := cmdEnrollWithTerminal([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "--code-file", privateCodeFile(t), "--url", server.URL}, enrollmentTerminal{})
			var output bytes.Buffer
			if exit := reportCommandError(&output, err); exit != 1 {
				t.Fatalf("exit = %d", exit)
			}
			if !strings.Contains(output.String(), want) || strings.Contains(output.String(), "could not be completed") {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}
