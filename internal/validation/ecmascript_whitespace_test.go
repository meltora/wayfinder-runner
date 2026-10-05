package validation

import "testing"

func TestIsECMAScriptBlankMatchesStringPrototypeTrim(t *testing.T) {
	for value, blank := range map[string]bool{
		"":                   true,
		"\ufeff":             true,
		"\ufeff\t":           true,
		"\u00a0\u1680\u3000": true,
		"\u2028\u2029\r\n":   true,
		"\v\f ":              true,
		"\u0085":             false,
		"\u200b":             false,
		"\u180e":             false,
		"\u0085\ufeff":       false,
		" x ":                false,
	} {
		if got := IsECMAScriptBlank(value); got != blank {
			t.Errorf("IsECMAScriptBlank(%+q) = %v, want %v", value, got, blank)
		}
	}
}

func TestIsECMAScriptWhitespaceExcludesUnicodeSpacesOutsideTheSet(t *testing.T) {
	for _, character := range []rune{0x0085, 0x180E, 0x200B, 0x200C, 0x200D, 0x2060, 'x'} {
		if IsECMAScriptWhitespace(character) {
			t.Errorf("U+%04X classified as ECMAScript whitespace", character)
		}
	}
	for _, character := range []rune{0x0009, 0x000A, 0x000B, 0x000C, 0x000D, 0x0020, 0x00A0, 0x1680, 0x2000, 0x200A, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF} {
		if !IsECMAScriptWhitespace(character) {
			t.Errorf("U+%04X not classified as ECMAScript whitespace", character)
		}
	}
}
