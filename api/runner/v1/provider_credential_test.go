package v1_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const testProviderToken = "ghs_redaction0000000000000000000000000000"

func validProviderCredential(now time.Time) v1.ProviderCredential {
	return v1.ProviderCredential{
		Kind:          v1.ProviderCredentialGitHubInstallationToken,
		Token:         testProviderToken,
		ExpiresAt:     now.Add(time.Hour),
		RepositoryIDs: []string{"123456789"},
	}
}

func TestProviderCredentialValidateAcceptsScopedToken(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	credential := validProviderCredential(now)
	if err := credential.Validate(now); err != nil {
		t.Fatal(err)
	}
	credential.Token = strings.Repeat("a", v1.ProviderCredentialMaxTokenLength)
	credential.RepositoryIDs = make([]string, v1.ProviderCredentialMaxRepositories)
	for index := range credential.RepositoryIDs {
		credential.RepositoryIDs[index] = fmt.Sprint(index + 1)
	}
	if err := credential.Validate(now); err != nil {
		t.Fatal(err)
	}
}

func TestProviderCredentialValidateRejectsWithoutEchoingToken(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]func(*v1.ProviderCredential){
		"unknown kind":        func(c *v1.ProviderCredential) { c.Kind = "github_app_jwt" },
		"empty token":         func(c *v1.ProviderCredential) { c.Token = "" },
		"overlong token":      func(c *v1.ProviderCredential) { c.Token = strings.Repeat("a", v1.ProviderCredentialMaxTokenLength+1) },
		"whitespace token":    func(c *v1.ProviderCredential) { c.Token = testProviderToken + " x" },
		"newline token":       func(c *v1.ProviderCredential) { c.Token = testProviderToken + "\nx" },
		"non-ASCII token":     func(c *v1.ProviderCredential) { c.Token = testProviderToken + "é" },
		"expired":             func(c *v1.ProviderCredential) { c.ExpiresAt = now },
		"missing expiry":      func(c *v1.ProviderCredential) { c.ExpiresAt = time.Time{} },
		"no repositories":     func(c *v1.ProviderCredential) { c.RepositoryIDs = nil },
		"named repository":    func(c *v1.ProviderCredential) { c.RepositoryIDs = []string{"octo/example"} },
		"zero repository":     func(c *v1.ProviderCredential) { c.RepositoryIDs = []string{"0"} },
		"negative repository": func(c *v1.ProviderCredential) { c.RepositoryIDs = []string{"-1"} },
		"too many repositories": func(c *v1.ProviderCredential) {
			c.RepositoryIDs = make([]string, v1.ProviderCredentialMaxRepositories+1)
			for index := range c.RepositoryIDs {
				c.RepositoryIDs[index] = fmt.Sprint(index + 1)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			credential := validProviderCredential(now)
			mutate(&credential)
			err := credential.Validate(now)
			if err == nil {
				t.Fatal("invalid credential accepted")
			}
			if strings.Contains(err.Error(), testProviderToken) {
				t.Fatalf("validation error echoes the token: %v", err)
			}
		})
	}
}

func TestProviderCredentialNeverFormatsItsToken(t *testing.T) {
	credential := validProviderCredential(time.Now())
	pointer := &credential
	envelope := v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "command-1", ProviderCredential: pointer}
	rendered := []string{
		fmt.Sprintf("%v", credential),
		fmt.Sprintf("%+v", credential),
		fmt.Sprintf("%#v", credential),
		fmt.Sprintf("%s", credential),
		fmt.Sprintf("%q", credential),
		fmt.Sprintf("%x", credential),
		fmt.Sprintf("%d", credential),
		fmt.Sprintf("%v", pointer),
		fmt.Sprintf("%+v", pointer),
		fmt.Sprintf("%#v", pointer),
		fmt.Sprintf("%v", envelope),
		fmt.Sprintf("%+v", envelope),
		fmt.Sprintf("%#v", envelope),
		fmt.Sprint(credential, pointer),
		credential.String(),
		credential.GoString(),
	}
	for _, value := range rendered {
		if strings.Contains(value, testProviderToken) || !strings.Contains(value, "[redacted]") {
			t.Fatalf("formatted credential = %q", value)
		}
	}

	for _, target := range []any{credential, pointer, envelope} {
		encoded, err := json.Marshal(target)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(testProviderToken)) || !bytes.Contains(encoded, []byte("[redacted]")) {
			t.Fatalf("json = %s", encoded)
		}
	}

	var logged bytes.Buffer
	for _, handler := range []slog.Handler{
		slog.NewJSONHandler(&logged, nil),
		slog.NewTextHandler(&logged, nil),
	} {
		logger := slog.New(handler)
		logger.Info("credential", "value", credential, "pointer", pointer, "envelope", envelope)
		logger.Info("credential", slog.Any("value", credential))
	}
	if strings.Contains(logged.String(), testProviderToken) || !strings.Contains(logged.String(), "[redacted]") {
		t.Fatalf("slog output = %s", logged.String())
	}
}

func TestEnvelopeStrictlyDecodesProviderCredential(t *testing.T) {
	var envelope v1.Envelope
	decodeStrictFixture(t, "testdata/source_publication_with_credential_v1_command.json", &envelope)
	credential := envelope.ProviderCredential
	if envelope.Type != v1.TypeCommand || credential == nil {
		t.Fatalf("envelope = %+v", envelope)
	}
	if credential.Kind != v1.ProviderCredentialGitHubInstallationToken ||
		credential.Token != "ghs_fixture000000000000000000000000000000" ||
		!credential.ExpiresAt.Equal(time.Date(2026, 8, 20, 17, 15, 0, 0, time.UTC)) ||
		len(credential.RepositoryIDs) != 1 || credential.RepositoryIDs[0] != "123456789" {
		t.Fatalf("credential fields were not decoded")
	}
	if err := credential.Validate(envelope.SentAt); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var command v1.CommandPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		t.Fatal(err)
	}
	if command.Command != v1.CmdSourcePublication || command.SourcePublication == nil {
		t.Fatalf("payload = %+v", command)
	}
	if err := validation.SourcePublicationOperation(*command.SourcePublication); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeRejectsUnknownProviderCredentialFields(t *testing.T) {
	raw := []byte(`{"version":1,"type":"command","id":"command-1","sent_at":"2030-01-01T00:00:00Z","payload":{},` +
		`"provider_credential":{"kind":"github_installation_token","token":"` + testProviderToken +
		`","expires_at":"2030-01-01T01:00:00Z","repository_ids":["1"],"scope":"admin"}}`)
	var envelope v1.Envelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&envelope)
	if err == nil {
		t.Fatal("unknown credential field accepted")
	}
	if strings.Contains(err.Error(), testProviderToken) {
		t.Fatalf("decode error echoes the token: %v", err)
	}
}

func TestProviderCredentialUsableAtRequiresOneMinuteOfLifetime(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	credential := validProviderCredential(now)
	if !credential.UsableAt(now) {
		t.Fatal("fresh credential is unusable")
	}
	credential.ExpiresAt = now.Add(v1.ProviderCredentialMinimumRemainingLifetime)
	if !credential.UsableAt(now) {
		t.Fatal("credential with exactly the minimum lifetime is unusable")
	}
	credential.ExpiresAt = now.Add(v1.ProviderCredentialMinimumRemainingLifetime - time.Second)
	if credential.UsableAt(now) {
		t.Fatal("near-expiry credential is usable")
	}
	credential = validProviderCredential(now)
	credential.Token = "bad token"
	if credential.UsableAt(now) {
		t.Fatal("invalid credential is usable")
	}
	var missing *v1.ProviderCredential
	if missing.UsableAt(now) {
		t.Fatal("missing credential is usable")
	}
}
