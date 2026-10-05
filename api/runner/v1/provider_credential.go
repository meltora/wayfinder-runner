package v1

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
)

const (
	ProviderCredentialGitHubInstallationToken  = "github_installation_token"
	ProviderCredentialMaxTokenLength           = 8192
	ProviderCredentialMaxRepositories          = 100
	ProviderCredentialMinimumRemainingLifetime = time.Minute
	providerCredentialMaxRepositoryIDDigits    = 19
	providerCredentialRedacted                 = "[redacted]"
)

var ErrInvalidProviderCredential = errors.New("invalid provider credential")

// ProviderCredential is an ephemeral, per-command provider token. It is
// attached beside the command payload at serve time, lives only in memory, and
// renders as [redacted] through fmt, slog and encoding/json.
type ProviderCredential struct {
	Kind          string    `json:"kind"`
	Token         string    `json:"token"`
	ExpiresAt     time.Time `json:"expires_at"`
	RepositoryIDs []string  `json:"repository_ids"`
}

func (c ProviderCredential) Validate(now time.Time) error {
	if c.Kind != ProviderCredentialGitHubInstallationToken {
		return ErrInvalidProviderCredential
	}
	if len(c.Token) == 0 || len(c.Token) > ProviderCredentialMaxTokenLength {
		return ErrInvalidProviderCredential
	}
	for index := 0; index < len(c.Token); index++ {
		if c.Token[index] < 0x21 || c.Token[index] > 0x7e {
			return ErrInvalidProviderCredential
		}
	}
	if !c.ExpiresAt.After(now) {
		return ErrInvalidProviderCredential
	}
	if len(c.RepositoryIDs) == 0 || len(c.RepositoryIDs) > ProviderCredentialMaxRepositories {
		return ErrInvalidProviderCredential
	}
	for _, repositoryID := range c.RepositoryIDs {
		if !validProviderRepositoryID(repositoryID) {
			return ErrInvalidProviderCredential
		}
	}
	return nil
}

func (c *ProviderCredential) UsableAt(now time.Time) bool {
	if c == nil || c.Validate(now) != nil {
		return false
	}
	return c.ExpiresAt.Sub(now) >= ProviderCredentialMinimumRemainingLifetime
}

func validProviderRepositoryID(value string) bool {
	if len(value) == 0 || len(value) > providerCredentialMaxRepositoryIDDigits || value[0] == '0' {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func (ProviderCredential) String() string {
	return providerCredentialRedacted
}

func (ProviderCredential) GoString() string {
	return "v1.ProviderCredential{" + providerCredentialRedacted + "}"
}

func (c ProviderCredential) Format(state fmt.State, verb rune) {
	if verb == 'v' && state.Flag('#') {
		_, _ = io.WriteString(state, c.GoString())
		return
	}
	_, _ = io.WriteString(state, providerCredentialRedacted)
}

func (ProviderCredential) MarshalJSON() ([]byte, error) {
	return []byte(`"` + providerCredentialRedacted + `"`), nil
}

func (ProviderCredential) LogValue() slog.Value {
	return slog.StringValue(providerCredentialRedacted)
}
