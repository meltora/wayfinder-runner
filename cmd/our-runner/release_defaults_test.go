package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func withStampedRelease(t *testing.T, channel, url string) {
	t.Helper()
	previousChannel, previousURL := daemon.Channel, daemon.DefaultControlPlaneURL
	daemon.Channel, daemon.DefaultControlPlaneURL = channel, url
	t.Cleanup(func() {
		daemon.Channel, daemon.DefaultControlPlaneURL = previousChannel, previousURL
	})
}

func enrollmentServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runners/enroll" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var request struct {
			CSR string `json:"csr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		cert, err := ca.SignCSR([]byte(request.CSR), "runner-7")
		if err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"runner_id": "runner-7", "certificate": string(cert), "ca_certificate": string(ca.CertPEM),
		})
	}))
	t.Cleanup(server.Close)
	withDefaultTransport(t, server.Client().Transport)
	return server, &calls
}

func privateCodeFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "enrollment-code")
	if err := os.WriteFile(path, []byte("enroll-once\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnrollSourceBuildRequiresURL(t *testing.T) {
	withStampedRelease(t, "", "")
	stateDir := filepath.Join(t.TempDir(), "state")
	err := cmdEnrollWithTerminal([]string{"--state-dir", stateDir, "--code-file", filepath.Join(t.TempDir(), "missing")}, enrollmentTerminal{})
	if err == nil || err.Error() != "enroll requires --url (this build has no default control plane)" {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Fatalf("state dir was created: %v", statErr)
	}
}

func TestEnrollChannelWithoutURLRequiresURL(t *testing.T) {
	withStampedRelease(t, "staging", "")
	err := cmdEnrollWithTerminal([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "--code-file", privateCodeFile(t)}, enrollmentTerminal{})
	if err == nil || err.Error() != "enroll requires --url (this build has no default control plane)" {
		t.Fatalf("err = %v", err)
	}
}

func TestEnrollUsesStampedControlPlane(t *testing.T) {
	server, calls := enrollmentServer(t)
	withStampedRelease(t, "develop", server.URL+"/")
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := cmdEnrollWithTerminal([]string{"--state-dir", stateDir, "--code-file", privateCodeFile(t)}, enrollmentTerminal{}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("exchange calls = %d", calls.Load())
	}
	if id, err := daemon.ReadRunnerID(stateDir); err != nil || id != "runner-7" {
		t.Fatalf("runner ID = %q, err = %v", id, err)
	}
}

func TestEnrollURLFlagOverridesStampedControlPlane(t *testing.T) {
	server, calls := enrollmentServer(t)
	var stampedCalls atomic.Int32
	stamped := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		stampedCalls.Add(1)
		http.Error(w, "wrong control plane", http.StatusTeapot)
	}))
	t.Cleanup(stamped.Close)
	withStampedRelease(t, "production", stamped.URL)
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := cmdEnrollWithTerminal([]string{"--state-dir", stateDir, "--code-file", privateCodeFile(t), "--url", server.URL}, enrollmentTerminal{}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if calls.Load() != 1 || stampedCalls.Load() != 0 {
		t.Fatalf("flag server calls = %d, stamped server calls = %d", calls.Load(), stampedCalls.Load())
	}
}

func TestEnrollInvalidStampRequiresURL(t *testing.T) {
	withStampedRelease(t, "develop", "http://control.example.test")
	err := cmdEnrollWithTerminal([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "--code-file", privateCodeFile(t)}, enrollmentTerminal{})
	if err == nil || err.Error() != "enroll requires --url (this build's default control plane is not an HTTPS origin)" {
		t.Fatalf("err = %v", err)
	}
	server, calls := enrollmentServer(t)
	if err := cmdEnrollWithTerminal([]string{"--state-dir", filepath.Join(t.TempDir(), "state"), "--code-file", privateCodeFile(t), "--url", server.URL}, enrollmentTerminal{}); err != nil {
		t.Fatalf("enroll with --url: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestVersionPrintsChannelAndControlPlane(t *testing.T) {
	previousVersion := daemon.Version
	daemon.Version = "1.4.0-develop.2"
	t.Cleanup(func() { daemon.Version = previousVersion })
	for name, tc := range map[string]struct {
		channel, url, want string
	}{
		"source":       {"", "", "our-runner 1.4.0-develop.2\nchannel: source\ncontrol plane: none (enroll requires --url)\n"},
		"stamped":      {"develop", "https://control.example.test/", "our-runner 1.4.0-develop.2\nchannel: develop\ncontrol plane: https://control.example.test\n"},
		"channel only": {"staging", "", "our-runner 1.4.0-develop.2\nchannel: staging\ncontrol plane: none (enroll requires --url)\n"},
		"invalid":      {"nightly", "https://control.example.test", "our-runner 1.4.0-develop.2\nchannel: invalid (this build has an unknown release channel)\ncontrol plane: none (enroll requires --url)\n"},
	} {
		t.Run(name, func(t *testing.T) {
			withStampedRelease(t, tc.channel, tc.url)
			var output bytes.Buffer
			writeVersion(&output)
			if output.String() != tc.want {
				t.Fatalf("version output:\n%s\nwant:\n%s", output.String(), tc.want)
			}
		})
	}
}
