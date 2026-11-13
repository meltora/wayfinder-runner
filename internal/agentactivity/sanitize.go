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
}

var redactionRules = []redactionRule{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), "credential"},
	{regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@]+@\S*`), "credential"},
	{regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{12,}`), "credential"},
	{regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abpr]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})`), "credential"},
	{regexp.MustCompile(`(?i)\b[a-z0-9_-]*(?:api[_-]?key|token|secret|password|passwd|authorization)[a-z0-9_-]*\s*[:=]\s*\S+`), "credential"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "personal_data"},
}

var absolutePathPattern = regexp.MustCompile(`(^|[\s"'(\[=,:])((?:~|[A-Za-z]:)?/[A-Za-z0-9._@+-]+(?:/[A-Za-z0-9._@+-]*)+)`)

func Sanitize(text string, maxBytes int) Sanitized {
	categories := map[string]struct{}{}
	count := 0
	for _, rule := range redactionRules {
		text = rule.pattern.ReplaceAllStringFunc(text, func(string) string {
			count++
			categories[rule.category] = struct{}{}
			return redact.Placeholder
		})
	}
	text, pathCount := redactAbsolutePaths(text)
	if pathCount > 0 {
		count += pathCount
		categories["filesystem_path"] = struct{}{}
	}
	message, truncated := truncate(normalizeWhitespace(text), maxBytes)
	values := make([]string, 0, len(categories))
	for category := range categories {
		values = append(values, category)
	}
	sort.Strings(values)
	return Sanitized{Message: message, Truncated: truncated, RedactionCount: count, Categories: values}
}

func redactAbsolutePaths(text string) (string, int) {
	matches := absolutePathPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, 0
	}
	var builder strings.Builder
	last := 0
	for _, match := range matches {
		builder.WriteString(text[last:match[4]])
		builder.WriteString(redact.Placeholder)
		last = match[5]
	}
	builder.WriteString(text[last:])
	return builder.String(), len(matches)
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
