package daemon

import (
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/example/app-runner/internal/hookspool"
)

const (
	providerOutputTailLines     = 16
	providerOutputLineBytes     = 256
	providerOutputTailBytes     = 2048
	providerOutputTailSeparator = " | "
	minimumRedactedValueBytes   = 8
	agentExitFailureMessage     = "agent process exited unsuccessfully"
)

var (
	unknownOptionPattern = regexp.MustCompile(`unknown option '([^']*)'`)
	rejectedOptionName   = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
)

type providerOutputTail struct {
	mu      sync.Mutex
	lines   []string
	secrets []string
}

func newProviderOutputTail(environment []string, credential string) *providerOutputTail {
	var secrets []string
	for _, entry := range environment {
		if _, value, ok := strings.Cut(entry, "="); ok && len(value) >= minimumRedactedValueBytes {
			secrets = append(secrets, value)
		}
	}
	if len(credential) >= minimumRedactedValueBytes {
		secrets = append(secrets, credential)
	}
	return &providerOutputTail{secrets: secrets}
}

func (t *providerOutputTail) add(raw []byte) {
	line := t.sanitize(string(raw))
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > providerOutputTailLines {
		t.lines = append([]string(nil), t.lines[len(t.lines)-providerOutputTailLines:]...)
	}
}

func (t *providerOutputTail) sanitize(line string) string {
	line = strings.ToValidUTF8(line, "�")
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, line)
	if redacted, _ := hookspool.Redact(map[string]any{"line": line})["line"].(string); redacted == "[redacted]" {
		return "[redacted]"
	}
	for _, secret := range t.secrets {
		line = strings.ReplaceAll(line, secret, "[redacted]")
	}
	return truncateUTF8Bytes(line, providerOutputLineBytes)
}

func (t *providerOutputTail) snapshot() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

func (t *providerOutputTail) String() string {
	lines := t.snapshot()
	for len(lines) > 0 {
		joined := strings.Join(lines, providerOutputTailSeparator)
		if len(joined) <= providerOutputTailBytes {
			return joined
		}
		lines = lines[1:]
	}
	return ""
}

func (t *providerOutputTail) rejectedOption() string {
	lines := t.snapshot()
	for index := len(lines) - 1; index >= 0; index-- {
		match := unknownOptionPattern.FindStringSubmatch(lines[index])
		if match != nil && rejectedOptionName.MatchString(match[1]) {
			return match[1]
		}
	}
	return ""
}

func agentExitFailure(rejectedOption string) string {
	if rejectedOption == "" {
		return agentExitFailureMessage
	}
	return agentExitFailureMessage + ": the provider CLI rejected option " + rejectedOption
}

func (jr *jobRunner) newProviderOutputTail() *providerOutputTail {
	credential := ""
	if jr.providerCredential != nil {
		credential = jr.providerCredential.Token
	}
	return newProviderOutputTail(jr.d.agentEnvironment(jr.d.cfg.Provider), credential)
}

func (jr *jobRunner) currentOutputTail() *providerOutputTail {
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	return jr.outputTail
}
