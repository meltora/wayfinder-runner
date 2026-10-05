package validation

import "strings"

var ecmaScriptWhitespace = map[rune]bool{
	0x0009: true, 0x000A: true, 0x000B: true, 0x000C: true, 0x000D: true, 0x0020: true, 0x00A0: true, 0x1680: true,
	0x2000: true, 0x2001: true, 0x2002: true, 0x2003: true, 0x2004: true, 0x2005: true, 0x2006: true, 0x2007: true,
	0x2008: true, 0x2009: true, 0x200A: true, 0x2028: true, 0x2029: true, 0x202F: true, 0x205F: true, 0x3000: true,
	0xFEFF: true,
}

func IsECMAScriptWhitespace(character rune) bool {
	return ecmaScriptWhitespace[character]
}

func IsECMAScriptBlank(value string) bool {
	return strings.TrimFunc(value, IsECMAScriptWhitespace) == ""
}
