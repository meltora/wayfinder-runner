package claudelogin

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

const (
	RefreshTokenEnv = "CLAUDE_CODE_OAUTH_REFRESH_TOKEN"
	RefreshScopeEnv = "CLAUDE_CODE_OAUTH_SCOPES"

	maxRefreshTokenBytes = 4096
	maxRefreshScopes     = 32
)

var (
	ErrNoRefreshGrant = errors.New("claude login has no usable refresh grant")

	scopePattern = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,128}$`)
)

func RefreshGrant(oauth json.RawMessage) (string, []string, error) {
	var fields struct {
		RefreshToken string   `json:"refreshToken"`
		Scopes       []string `json:"scopes"`
	}
	if err := json.Unmarshal(oauth, &fields); err != nil {
		return "", nil, ErrUnparsable
	}
	if err := CheckRefreshGrant(fields.RefreshToken, fields.Scopes); err != nil {
		return "", nil, err
	}
	return fields.RefreshToken, fields.Scopes, nil
}

func CheckRefreshGrant(token string, scopes []string) error {
	if token == "" || len(token) > maxRefreshTokenBytes || strings.ContainsFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r > unicode.MaxASCII
	}) {
		return ErrNoRefreshGrant
	}
	if len(scopes) == 0 || len(scopes) > maxRefreshScopes {
		return ErrNoRefreshGrant
	}
	for _, scope := range scopes {
		if !scopePattern.MatchString(scope) {
			return ErrNoRefreshGrant
		}
	}
	return nil
}
