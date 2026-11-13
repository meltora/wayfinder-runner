package agentactivity

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

func canonicalize(text string) string {
	return stripFormatCharacters(norm.NFKC.String(stripFormatCharacters(strings.ToValidUTF8(text, " "))))
}

func stripFormatCharacters(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}
