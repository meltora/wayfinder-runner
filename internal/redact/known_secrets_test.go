package redact

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestKnownSecretsRedactsRememberedValuesAndEncodings(t *testing.T) {
	const secret = "known-secret-value-0123456789"
	t.Cleanup(ForgetSecretsForTest)
	RememberSecret(secret)
	RememberSecret("short")
	text := "a " + secret + " b " + base64.StdEncoding.EncodeToString([]byte(secret)) + " short"
	got := KnownSecrets(text)
	if strings.Contains(got, secret) || strings.Contains(got, base64.StdEncoding.EncodeToString([]byte(secret))) {
		t.Fatalf("remembered secret leaked: %q", got)
	}
	if !strings.Contains(got, "short") {
		t.Fatal("values shorter than the minimum must not be remembered")
	}
	if err := KnownSecretsError(errors.New("failed with " + secret)); strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestRememberedSecretsReturnsSortedSnapshot(t *testing.T) {
	t.Cleanup(ForgetSecretsForTest)
	ForgetSecretsForTest()
	RememberSecret("second-secret-value-0123")
	RememberSecret("first-secret-value-0123")
	RememberSecret("short")
	got := RememberedSecrets()
	if len(got) != 2 || got[0] != "first-secret-value-0123" || got[1] != "second-secret-value-0123" {
		t.Fatalf("got %q", got)
	}
	got[0] = "changed"
	if RememberedSecrets()[0] != "first-secret-value-0123" {
		t.Fatal("snapshot aliases the registry")
	}
}

func TestKnownSecretsLeavesTextUnchangedWithoutSecrets(t *testing.T) {
	t.Cleanup(ForgetSecretsForTest)
	ForgetSecretsForTest()
	if got := KnownSecrets("plain text"); got != "plain text" {
		t.Fatalf("got %q", got)
	}
	if KnownSecretsError(nil) != nil {
		t.Fatal("nil error changed")
	}
}
