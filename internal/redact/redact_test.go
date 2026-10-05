package redact

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const testToken = "ghs_redact00000000000000000000000000000"

func TestTokenScrubsRawAndEncodedForms(t *testing.T) {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + testToken))
	input := "remote: bad token " + testToken + "\nAuthorization: Basic " + basic + "\nAuthorization: Bearer " + testToken
	output := Token(input, testToken)
	if strings.Contains(output, testToken) || strings.Contains(output, basic) {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, Placeholder) {
		t.Fatalf("output = %q", output)
	}
	if Token("unrelated", "") != "unrelated" {
		t.Fatal("empty token changed text")
	}
}

func TestErrorScrubsMessageAndKeepsIdentity(t *testing.T) {
	sentinel := errors.New("sentinel")
	wrapped := fmt.Errorf("git push: %w: fatal: %s", sentinel, testToken)
	scrubbed := Error(wrapped, testToken)
	if strings.Contains(scrubbed.Error(), testToken) || !strings.Contains(scrubbed.Error(), Placeholder) {
		t.Fatalf("error = %q", scrubbed.Error())
	}
	if !errors.Is(scrubbed, sentinel) {
		t.Fatal("scrubbed error lost its identity")
	}
	if errors.Unwrap(scrubbed) != nil {
		t.Fatal("scrubbed error exposes the unscrubbed cause")
	}
	clean := errors.New("clean")
	if Error(clean, testToken) != clean || Error(nil, testToken) != nil {
		t.Fatal("clean errors must pass through")
	}
}
