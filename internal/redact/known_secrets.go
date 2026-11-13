package redact

import (
	"sort"
	"sync"
)

const minimumKnownSecretLength = 12

var (
	knownSecretsMu sync.RWMutex
	knownSecrets   = map[string]struct{}{}
)

func RememberSecret(value string) {
	if len(value) < minimumKnownSecretLength {
		return
	}
	knownSecretsMu.Lock()
	knownSecrets[value] = struct{}{}
	knownSecretsMu.Unlock()
}

func RememberedSecrets() []string {
	knownSecretsMu.RLock()
	secrets := make([]string, 0, len(knownSecrets))
	for secret := range knownSecrets {
		secrets = append(secrets, secret)
	}
	knownSecretsMu.RUnlock()
	sort.Strings(secrets)
	return secrets
}

func KnownSecrets(text string) string {
	if text == "" {
		return text
	}
	knownSecretsMu.RLock()
	defer knownSecretsMu.RUnlock()
	for secret := range knownSecrets {
		text = Token(text, secret)
	}
	return text
}

func KnownSecretsError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	scrubbed := KnownSecrets(message)
	if scrubbed == message {
		return err
	}
	return scrubbedError{message: scrubbed, cause: err}
}

func ForgetSecretsForTest() {
	knownSecretsMu.Lock()
	knownSecrets = map[string]struct{}{}
	knownSecretsMu.Unlock()
}
