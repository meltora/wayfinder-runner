package agentactivity

import "strings"

var confusableLetters = map[rune]string{
	'a': "aаα",
	'c': "cсϲ",
	'e': "eеε",
	'h': "hһ",
	'i': "iіιӏ",
	'j': "jј",
	'k': "kкκ",
	'o': "oоο",
	'p': "pрρ",
	's': "sѕ",
	'x': "xхχ",
	'y': "yуγ",
	'd': "dԁ",
	'w': "wԝ",
}

func confusable(word string) string {
	var builder strings.Builder
	for _, r := range word {
		if letters, ok := confusableLetters[r]; ok {
			builder.WriteString("[" + letters + "]")
			continue
		}
		builder.WriteString(regexpQuoteRune(r))
	}
	return builder.String()
}

func confusableAny(words ...string) string {
	parts := make([]string, len(words))
	for i, word := range words {
		parts[i] = confusable(word)
	}
	return "(?:" + strings.Join(parts, "|") + ")"
}

func regexpQuoteRune(r rune) string {
	if strings.ContainsRune(`\.+*?()|[]{}^$-`, r) {
		return `\` + string(r)
	}
	return string(r)
}
