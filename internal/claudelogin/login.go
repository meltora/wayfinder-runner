package claudelogin

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
)

const oauthKey = "claudeAiOauth"

var (
	ErrUnparsable   = errors.New("claude login is not valid JSON")
	ErrNoOAuth      = errors.New("claude login has no claudeAiOauth object")
	ErrNoAccess     = errors.New("claude login has no access token")
	ErrBadExpiry    = errors.New("claude login has no numeric expiresAt")
	ErrDuplicateKey = errors.New("claude login has a duplicate top-level key")
)

type Account struct {
	AccountUUID      string
	OrganizationUUID string
}

func (a Account) Known() bool {
	return a.AccountUUID != "" && a.OrganizationUUID != ""
}

type Login struct {
	OAuth       json.RawMessage
	RefreshHash [32]byte
	HasRefresh  bool
	ExpiresAt   int64
	Account     Account
}

type Reason string

const (
	ReasonOK              Reason = "ok"
	ReasonUnchanged       Reason = "unchanged"
	ReasonOlder           Reason = "older"
	ReasonHostChanged     Reason = "host_changed"
	ReasonAccountMismatch Reason = "account_mismatch"
	ReasonUnparsable      Reason = "unparsable"
)

type member struct {
	key   string
	value json.RawMessage
}

func members(data []byte) ([]member, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrUnparsable
	}
	var out []member
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrUnparsable
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrUnparsable
		}
		if seen[key] {
			return nil, ErrDuplicateKey
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrUnparsable
		}
		out = append(out, member{key: key, value: value})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrUnparsable
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrUnparsable
	}
	return out, nil
}

func encodeMembers(list []member) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, item := range list {
		if index > 0 {
			buffer.WriteByte(',')
		}
		var key bytes.Buffer
		encoder := json.NewEncoder(&key)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(item.key); err != nil {
			return nil, ErrUnparsable
		}
		buffer.Write(bytes.TrimRight(key.Bytes(), "\n"))
		buffer.WriteByte(':')
		buffer.Write(item.value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func oauthObject(data []byte) (json.RawMessage, error) {
	list, err := members(data)
	if err != nil {
		return nil, err
	}
	for _, item := range list {
		if item.key == oauthKey {
			if err := checkObject(item.value); err != nil {
				return nil, ErrNoOAuth
			}
			return item.value, nil
		}
	}
	return nil, ErrNoOAuth
}

func checkObject(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return ErrUnparsable
	}
	return nil
}

func ParseCredentials(data []byte) (Login, error) {
	oauth, err := oauthObject(data)
	if err != nil {
		return Login{}, err
	}
	var fields struct {
		AccessToken      *string          `json:"accessToken"`
		RefreshToken     *string          `json:"refreshToken"`
		ExpiresAt        *json.RawMessage `json:"expiresAt"`
		AccountUUID      string           `json:"accountUuid"`
		OrganizationUUID string           `json:"organizationUuid"`
	}
	if err := json.Unmarshal(oauth, &fields); err != nil {
		return Login{}, ErrUnparsable
	}
	if fields.AccessToken == nil || *fields.AccessToken == "" {
		return Login{}, ErrNoAccess
	}
	if fields.ExpiresAt == nil {
		return Login{}, ErrBadExpiry
	}
	var expiresAt int64
	if err := json.Unmarshal(*fields.ExpiresAt, &expiresAt); err != nil {
		return Login{}, ErrBadExpiry
	}
	login := Login{
		OAuth:     oauth,
		ExpiresAt: expiresAt,
		Account:   Account{AccountUUID: fields.AccountUUID, OrganizationUUID: fields.OrganizationUUID},
	}
	if fields.RefreshToken != nil && *fields.RefreshToken != "" {
		login.HasRefresh = true
		login.RefreshHash = sha256.Sum256([]byte(*fields.RefreshToken))
	}
	return login, nil
}

func ParseAccount(claudeJSON []byte) (Account, error) {
	var state struct {
		OAuthAccount *struct {
			AccountUUID      string `json:"accountUuid"`
			OrganizationUUID string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(claudeJSON, &state); err != nil {
		return Account{}, ErrUnparsable
	}
	if state.OAuthAccount == nil {
		return Account{}, nil
	}
	return Account{AccountUUID: state.OAuthAccount.AccountUUID, OrganizationUUID: state.OAuthAccount.OrganizationUUID}, nil
}

func Decide(candidate, host Login, baseline [32]byte, candidateAccount, hostAccount Account) Reason {
	if candidate.OAuth == nil || host.OAuth == nil {
		return ReasonUnparsable
	}
	if !candidate.HasRefresh || candidate.RefreshHash == baseline {
		return ReasonUnchanged
	}
	if candidate.ExpiresAt <= host.ExpiresAt {
		return ReasonOlder
	}
	if !host.HasRefresh || host.RefreshHash != baseline {
		return ReasonHostChanged
	}
	if !sameAccount(candidate.Account, host.Account, candidateAccount, hostAccount) {
		return ReasonAccountMismatch
	}
	return ReasonOK
}

func sameAccount(candidateOAuth, hostOAuth, candidateState, hostState Account) bool {
	switch {
	case candidateOAuth.Known() && hostOAuth.Known():
		return candidateOAuth == hostOAuth
	case candidateState.Known() && hostState.Known():
		return candidateState == hostState
	}
	none := Account{}
	return candidateOAuth == none && hostOAuth == none && candidateState == none && hostState == none
}

func MergeCredentials(target []byte, oauth json.RawMessage) ([]byte, error) {
	if err := checkObject(oauth); err != nil {
		return nil, err
	}
	list, err := members(target)
	if err != nil {
		return nil, err
	}
	replaced := false
	for index := range list {
		if list[index].key == oauthKey {
			list[index].value = oauth
			replaced = true
		}
	}
	if !replaced {
		list = append(list, member{key: oauthKey, value: oauth})
	}
	return encodeMembers(list)
}

func JobCredentials(host []byte) ([]byte, error) {
	oauth, err := oauthObject(host)
	if err != nil {
		return nil, err
	}
	return encodeMembers([]member{{key: oauthKey, value: oauth}})
}
