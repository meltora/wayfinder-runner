package claudelogin

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestRefreshGrantReadsTheTokenAndScopes(t *testing.T) {
	token, scopes, err := RefreshGrant(json.RawMessage(`{"accessToken":"a","refreshToken":"fake-refresh","expiresAt":1,"scopes":["user:profile","user:inference"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if token != "fake-refresh" || !slices.Equal(scopes, []string{"user:profile", "user:inference"}) {
		t.Fatalf("grant = %q %v", token, scopes)
	}
}

func TestRefreshGrantRejectsAnUnusableGrant(t *testing.T) {
	for name, oauth := range map[string]string{
		"no refresh token":     `{"accessToken":"a","scopes":["user:inference"]}`,
		"no scopes":            `{"refreshToken":"fake-refresh"}`,
		"newline in the token": `{"refreshToken":"fake\nCLAUDE_CONFIG_DIR=/","scopes":["user:inference"]}`,
		"space in the token":   `{"refreshToken":"fake refresh","scopes":["user:inference"]}`,
		"space in a scope":     `{"refreshToken":"fake-refresh","scopes":["user:inference user:profile"]}`,
		"empty scope":          `{"refreshToken":"fake-refresh","scopes":[""]}`,
		"scopes not a list":    `{"refreshToken":"fake-refresh","scopes":"user:inference"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := RefreshGrant(json.RawMessage(oauth)); err == nil {
				t.Fatal("an unusable grant was accepted")
			} else if !errors.Is(err, ErrNoRefreshGrant) && !errors.Is(err, ErrUnparsable) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
