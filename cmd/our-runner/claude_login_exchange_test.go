package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/claudelogin"
)

func claudeLoginExchangeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	login := `{"claudeAiOauth":{"accessToken":"job-access","refreshToken":"job-refresh","expiresAt":100}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(login), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestClaudeLoginExchangeWritesTheResultToStdout(t *testing.T) {
	home := claudeLoginExchangeHome(t)
	request, err := json.Marshal(claudelogin.ExchangeRequest{
		Host:     json.RawMessage(`{"accessToken":"host-access","refreshToken":"host-refresh","expiresAt":200}`),
		Baseline: sha256.Sum256([]byte("job-refresh")),
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runClaudeLoginExchange(nil, bytes.NewReader(request), &stdout, &stderr, home); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	result, err := claudelogin.DecodeExchangeResult(bytes.TrimSpace(stdout.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != claudelogin.ReasonOK || !result.Replaced {
		t.Fatalf("result = %+v", result)
	}
}

func TestClaudeLoginExchangeRejectsBadInputWithoutEchoingIt(t *testing.T) {
	home := claudeLoginExchangeHome(t)
	cases := map[string]struct {
		args  []string
		input string
	}{
		"oversize":       {input: `{"host":"` + strings.Repeat("x", claudelogin.MaxExchangeBytes) + `"}`},
		"unknown field":  {input: `{"host":{"accessToken":"secret-host-token","expiresAt":2},"token":"secret-host-token"}`},
		"unusable host":  {input: `{"host":{"refreshToken":"secret-host-token"}}`},
		"extra argument": {args: []string{"secret-host-token"}, input: `{}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runClaudeLoginExchange(tc.args, strings.NewReader(tc.input), &stdout, &stderr, home); code == 0 {
				t.Fatal("bad input was accepted")
			}
			if stdout.Len() != 0 {
				t.Fatal("a failed exchange wrote to stdout")
			}
			if strings.Contains(stderr.String(), "secret-host-token") || stderr.Len() > 512 {
				t.Fatalf("stderr echoes the input: %q", stderr.String())
			}
		})
	}
}
