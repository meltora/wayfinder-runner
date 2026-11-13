package daemon

import "strings"

func knownSecretValues(environment []string, credential string) []string {
	var secrets []string
	for _, entry := range environment {
		if _, value, ok := strings.Cut(entry, "="); ok && len(value) >= minimumRedactedValueBytes {
			secrets = append(secrets, value)
		}
	}
	if len(credential) >= minimumRedactedValueBytes {
		secrets = append(secrets, credential)
	}
	return secrets
}

func (jr *jobRunner) knownSecrets() []string {
	credential := ""
	if jr.providerCredential != nil {
		credential = jr.providerCredential.Token
	}
	return knownSecretValues(jr.d.agentEnvironment(jr.d.cfg.Provider), credential)
}
