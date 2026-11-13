package agentactivity

import (
	"regexp"
	"strings"

	"github.com/example/app-runner/internal/redact"
)

const (
	pathSegment   = "[^\\s/\\\\\"'`<>()\\[\\]{}|,;]"
	pathSeparator = `[/\\]`
	pathBoundary  = `(^|[^\pL\pN._@+/\\-])`
	urlBoundary   = `(^|[^\pL\pN._@+/\\:-])`
)

var pathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|[^\pL\pN_])((?i:file):(?:` + pathSeparator + `|` + pathSegment + `)+)`),
	regexp.MustCompile(urlBoundary + `(` + pathSeparator + `{2}` + pathSegment + `+(?:` + pathSeparator + pathSegment + `*)*)`),
	regexp.MustCompile(pathBoundary + `(~` + pathSeparator + `(?:` + pathSeparator + `|` + pathSegment + `)*)`),
	regexp.MustCompile(pathBoundary + `((?:[A-Za-z]:)?` + pathSeparator + pathSegment + `+(?:` + pathSeparator + pathSegment + `*)+)`),
}

func redactAbsolutePaths(text string) (string, int) {
	total := 0
	for _, pattern := range pathPatterns {
		var count int
		text, count = replacePathMatches(pattern, text)
		total += count
	}
	return text, total
}

var pathContinuation = regexp.MustCompile(`^ +` + pathSegment + `+(?:` + pathSeparator + pathSegment + `*)+`)

func replacePathMatches(pattern *regexp.Regexp, text string) (string, int) {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, 0
	}
	var builder strings.Builder
	last, count := 0, 0
	for _, match := range matches {
		if match[4] < last {
			continue
		}
		end := match[5]
		for {
			next := pathContinuation.FindStringIndex(text[end:])
			if next == nil {
				break
			}
			end += next[1]
		}
		builder.WriteString(text[last:match[4]])
		builder.WriteString(redact.Placeholder)
		last = end
		count++
	}
	builder.WriteString(text[last:])
	return builder.String(), count
}

func replaceGroup(pattern *regexp.Regexp, group int, text string) (string, int) {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, 0
	}
	var builder strings.Builder
	last := 0
	for _, match := range matches {
		builder.WriteString(text[last:match[2*group]])
		builder.WriteString(redact.Placeholder)
		last = match[2*group+1]
	}
	builder.WriteString(text[last:])
	return builder.String(), len(matches)
}
