package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
)

type recordedExchange struct {
	name     string
	args     []string
	stdin    []byte
	deadline time.Duration
}

func exchangeTestOwnership(t *testing.T, layout ownershipLayout, calls *[]recordedExchange, stdout []byte, runErr error) (DockerOwnership, string) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "our-runner")
	if err := os.WriteFile(exe, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	ownership := DockerOwnership{
		Mapping: testRootlessMapping, Image: testOwnershipImage, StateDir: layout.state,
		EffectiveUID: func() int { return 1000 },
		Executable:   func() (string, error) { return exe, nil },
		RunIO: func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
			deadline, _ := ctx.Deadline()
			*calls = append(*calls, recordedExchange{name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...), deadline: time.Until(deadline)})
			return stdout, runErr
		},
	}
	return ownership, exe
}

var exchangeTestRequest = claudelogin.ExchangeRequest{
	Host:        json.RawMessage(`{"accessToken":"fake-host-access","refreshToken":"fake-host-refresh","expiresAt":200}`),
	HostAccount: claudelogin.Account{AccountUUID: "a", OrganizationUUID: "o"},
	Baseline:    sha256.Sum256([]byte("fake-job-refresh")),
}

func TestExchangeLoginArgvIsExactlyTheContract(t *testing.T) {
	layout := newOwnershipLayout(t)
	var calls []recordedExchange
	ownership, exe := exchangeTestOwnership(t, layout, &calls, []byte(`{"job":{"accessToken":"x","expiresAt":1},"jobAccount":{"AccountUUID":"","OrganizationUUID":""},"replaced":true,"reason":"ok"}`+"\n"), nil)
	result, err := ownership.ExchangeLogin(context.Background(), "runner-1", "attempt-1", layout.home, exchangeTestRequest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != claudelogin.ReasonOK || !result.Replaced {
		t.Fatalf("result = %+v", result)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	name := ContainerName("runner-1", "attempt-1-own-login-exchange")
	want := []string{
		"run", "--rm", "-i", "--user", "10001:10001", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--network", "none", "--read-only", "--log-driver", "none",
		"--pids-limit", "64", "--memory", "128m", "--name", name,
		"--label", "com.ourplatform.runner-id=runner-1", "--label", "com.ourplatform.attempt-id=attempt-1",
		"-v", layout.home + ":/home/runner:rw",
		"-v", exe + ":/opt/our-runner-helper/our-runner:ro",
		"--entrypoint", "/opt/our-runner-helper/our-runner",
		testOwnershipImage, "claude-login-exchange",
	}
	if calls[0].name != name || !slices.Equal(calls[0].args, want) {
		t.Fatalf("argv = %q", calls[0].args)
	}
	for _, arg := range calls[0].args {
		if strings.Contains(arg, "fake-host") || arg == "-e" || arg == "--env" || arg == "--env-file" {
			t.Fatalf("argv carries the login: %q", arg)
		}
	}
	sent, err := claudelogin.DecodeExchangeRequest(calls[0].stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(sent.Host) != string(exchangeTestRequest.Host) || sent.Baseline != exchangeTestRequest.Baseline || sent.HostAccount != exchangeTestRequest.HostAccount {
		t.Fatalf("stdin request = %+v", sent)
	}
	if calls[0].deadline <= 0 || calls[0].deadline > 30*time.Second {
		t.Fatalf("deadline = %s", calls[0].deadline)
	}
}

func TestExchangeLoginRefusesUnsafeInputs(t *testing.T) {
	layout := newOwnershipLayout(t)
	cases := map[string]func(*DockerOwnership) string{
		"home outside job homes": func(*DockerOwnership) string { return layout.workspace },
		"check home":             func(*DockerOwnership) string { return layout.checkHome },
		"identity mapping": func(o *DockerOwnership) string {
			o.Mapping = UserMapping{Mode: UserMappingIdentity, ContainerUID: 1000, ContainerGID: 1000, HostUID: 1000, HostGID: 1000}
			return layout.home
		},
		"executable with a colon": func(o *DockerOwnership) string {
			o.Executable = func() (string, error) { return "/opt/a:b/our-runner", nil }
			return layout.home
		},
		"relative executable": func(o *DockerOwnership) string {
			o.Executable = func() (string, error) { return "our-runner", nil }
			return layout.home
		},
		"executable lookup fails": func(o *DockerOwnership) string {
			o.Executable = func() (string, error) { return "", errors.New("no executable") }
			return layout.home
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var calls []recordedExchange
			ownership, _ := exchangeTestOwnership(t, layout, &calls, nil, nil)
			home := mutate(&ownership)
			if _, err := ownership.ExchangeLogin(context.Background(), "runner-1", "attempt-1", home, exchangeTestRequest); err == nil {
				t.Fatal("unsafe input was accepted")
			}
			if len(calls) != 0 {
				t.Fatal("the helper ran")
			}
		})
	}
}

func TestExchangeLoginRejectsABadHelperReply(t *testing.T) {
	layout := newOwnershipLayout(t)
	cases := map[string]struct {
		stdout []byte
		err    error
	}{
		"helper failed":  {stdout: []byte(`{"job":{"accessToken":"fake-job-access"}}`), err: errors.New("exit status 1")},
		"garbage reply":  {stdout: []byte(`fake-job-access`)},
		"unknown field":  {stdout: []byte(`{"reason":"ok","token":"fake-job-access"}`)},
		"oversize reply": {stdout: []byte(`{"reason":"` + strings.Repeat("x", claudelogin.MaxExchangeBytes) + `"}`)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var calls []recordedExchange
			ownership, _ := exchangeTestOwnership(t, layout, &calls, tc.stdout, tc.err)
			_, err := ownership.ExchangeLogin(context.Background(), "runner-1", "attempt-1", layout.home, exchangeTestRequest)
			if err == nil {
				t.Fatal("a bad reply was accepted")
			}
			if strings.Contains(err.Error(), "fake-") {
				t.Fatalf("the error carries the login: %v", err)
			}
		})
	}
}
