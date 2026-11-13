package agentactivity

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/example/app-runner/internal/redact"
)

const ellipsis = "…"

type Sanitized struct {
	Message        string
	Truncated      bool
	RedactionCount int
	Categories     []string
}

type redactionRule struct {
	pattern  *regexp.Regexp
	category string
	group    int
}

var credentialKeyName = confusableAny("token", "secret", "password", "passwd", "authorization", "credential", "api_key", "api-key", "apikey")

var tokenPrefixBody = `(?:` +
	confusable("sk-ant-") + `[A-Za-z0-9_-]{12,}|` +
	confusable("sk-") + `[A-Za-z0-9_-]{12,}|` +
	confusable("gh") + confusableAny("p", "o", "u", "s", "r") + `_[A-Za-z0-9]{20,}|` +
	confusable("github_pat_") + `[A-Za-z0-9_]{20,}|` +
	confusable("xox") + `[abprse]-[A-Za-z0-9-]{10,}|` +
	`(?:AKIA|ASIA)[0-9A-Z]{16})`

var redactionRules = []redactionRule{
	{regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@]+@\S*`), "credential", 0},
	{regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{12,}`), "credential", 0},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`), "credential", 0},
	{regexp.MustCompile(`(?i)["']?[\pL\pN_-]*` + credentialKeyName + `[\pL\pN_-]*["']?\s*[:=]\s*(?:"(?:[^"\\]|\\(?s:.))*"?|'(?:[^'\\]|\\(?s:.))*'?|\S+)`), "credential", 0},
	{regexp.MustCompile(`(^|[^\pL\pN_])(` + tokenPrefixBody + `)`), "credential", 2},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "personal_data", 0},
}

var pemBlockPattern = regexp.MustCompile(`(?i)-----\s*BEGIN`)

var highEntropyPattern = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)

func Sanitize(text string, maxBytes int, knownSecrets ...string) Sanitized {
	text = canonicalize(text)
	if pemBlockPattern.MatchString(text) {
		return Sanitized{Categories: []string{}}
	}
	categories := map[string]struct{}{}
	count := 0
	for _, secret := range knownSecretForms(knownSecrets) {
		before := strings.Count(text, redact.Placeholder)
		text = redact.Token(text, secret)
		if added := strings.Count(text, redact.Placeholder) - before; added > 0 {
			count += added
			categories["credential"] = struct{}{}
		}
	}
	for _, rule := range redactionRules {
		var added int
		text, added = replaceGroup(rule.pattern, rule.group, text)
		if added > 0 {
			count += added
			categories[rule.category] = struct{}{}
		}
	}
	text, pathCount := redactAbsolutePaths(text)
	if pathCount > 0 {
		count += pathCount
		categories["filesystem_path"] = struct{}{}
	}
	text = highEntropyPattern.ReplaceAllStringFunc(text, func(match string) string {
		if !mixedCharacterClasses(match) {
			return match
		}
		count++
		categories["credential"] = struct{}{}
		return redact.Placeholder
	})
	message, truncated := truncate(normalizeWhitespace(text), maxBytes)
	values := make([]string, 0, len(categories))
	for category := range categories {
		values = append(values, category)
	}
	sort.Strings(values)
	return Sanitized{Message: message, Truncated: truncated, RedactionCount: count, Categories: values}
}

func knownSecretForms(secrets []string) []string {
	forms := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		forms = append(forms, secret)
		if canonical := canonicalize(secret); canonical != secret {
			forms = append(forms, canonical)
		}
	}
	return forms
}

func mixedCharacterClasses(value string) bool {
	digit, letter := false, false
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case unicode.IsLetter(r):
			letter = true
		}
	}
	return digit && letter
}

func normalizeWhitespace(text string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, " "))
	return strings.Join(strings.Fields(cleaned), " ")
}

func truncate(text string, maxBytes int) (string, bool) {
	if len(text) <= maxBytes {
		return text, false
	}
	limit := maxBytes - len(ellipsis)
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return strings.TrimSpace(text[:limit]) + ellipsis, true
}
