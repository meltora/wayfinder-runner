package redact

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

const Placeholder = "[redacted]"

const gitHubTokenUsername = "x-access-token"

func Token(text, token string) string {
	if token == "" || text == "" {
		return text
	}
	variants := []string{
		base64.StdEncoding.EncodeToString([]byte(gitHubTokenUsername + ":" + token)),
		base64.StdEncoding.EncodeToString([]byte(token)),
		url.QueryEscape(token),
		url.PathEscape(token),
		token,
	}
	for _, variant := range variants {
		if variant != "" {
			text = strings.ReplaceAll(text, variant, Placeholder)
		}
	}
	return text
}

func Error(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	message := err.Error()
	scrubbed := Token(message, token)
	if scrubbed == message {
		return err
	}
	return scrubbedError{message: scrubbed, cause: err}
}

type scrubbedError struct {
	message string
	cause   error
}

func (e scrubbedError) Error() string { return e.message }

func (e scrubbedError) Is(target error) bool {
	return target != nil && errors.Is(e.cause, target)
}
