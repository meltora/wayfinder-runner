package plancommand

import (
	"fmt"
	"strings"
)

const maxPipelineStages = 4

const unquotedPatternCharacters = "*?[]^~"

type shellLexer struct {
	source string
	stages [][]string
	words  []string
	text   strings.Builder
	inWord bool
	prev   byte
}

func splitPipeline(command string) ([][]string, error) {
	if err := rejectControlCharacters(command); err != nil {
		return nil, err
	}
	lexer := &shellLexer{source: command}
	for i := 0; i < len(command); i++ {
		next, err := lexer.consume(i)
		if err != nil {
			return nil, err
		}
		i = next
	}
	lexer.endWord()
	if err := lexer.endStage(true); err != nil {
		return nil, err
	}
	if len(lexer.stages) > maxPipelineStages {
		return nil, fmt.Errorf("pipelines are limited to %d commands", maxPipelineStages)
	}
	return lexer.stages, nil
}

func rejectControlCharacters(command string) error {
	for i := 0; i < len(command); i++ {
		c := command[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return fmt.Errorf("control characters, newlines and NUL are not allowed")
		}
	}
	return nil
}

func (l *shellLexer) consume(i int) (int, error) {
	c := l.source[i]
	switch {
	case c == ' ' || c == '\t':
		l.endWord()
		return i, nil
	case c == '\'':
		return l.consumeSingleQuoted(i)
	case c == '"':
		return l.consumeDoubleQuoted(i)
	case c == '|':
		return i, l.consumePipe(i)
	case strings.IndexByte(";&<>(){}$`\\#!", c) >= 0:
		return i, fmt.Errorf("shell metacharacter %q is not allowed", c)
	}
	if err := l.checkUnquoted(c); err != nil {
		return i, err
	}
	l.appendUnquoted(c)
	return i, nil
}

func (l *shellLexer) checkUnquoted(c byte) error {
	if strings.IndexByte(unquotedPatternCharacters, c) >= 0 {
		return fmt.Errorf("unquoted %q is not allowed because the shell may expand it; quote the argument (for example 'HEAD~1' or '*.go') and use the Glob tool to list files", c)
	}
	if c == '=' && !(l.inWord && isNameByte(l.prev)) {
		return fmt.Errorf("unquoted %q at the start of a word or value is not allowed because the shell may expand it", c)
	}
	return nil
}

func (l *shellLexer) consumeSingleQuoted(i int) (int, error) {
	end := strings.IndexByte(l.source[i+1:], '\'')
	if end < 0 {
		return i, fmt.Errorf("unterminated single quote")
	}
	closing := i + 1 + end
	if closing+1 < len(l.source) && l.source[closing+1] == '\'' {
		return i, fmt.Errorf("adjacent single quotes are not allowed because shells disagree on their meaning")
	}
	l.appendQuoted(l.source[i+1 : closing])
	return closing, nil
}

func (l *shellLexer) consumeDoubleQuoted(i int) (int, error) {
	end := strings.IndexByte(l.source[i+1:], '"')
	if end < 0 {
		return i, fmt.Errorf("unterminated double quote")
	}
	content := l.source[i+1 : i+1+end]
	if strings.ContainsAny(content, "$`\\!") {
		return i, fmt.Errorf("expansions, escapes and history characters inside double quotes are not allowed")
	}
	l.appendQuoted(content)
	return i + 1 + end, nil
}

func (l *shellLexer) consumePipe(i int) error {
	if i+1 < len(l.source) && (l.source[i+1] == '|' || l.source[i+1] == '&') {
		return fmt.Errorf("only simple pipelines are allowed")
	}
	l.endWord()
	return l.endStage(false)
}

func (l *shellLexer) appendUnquoted(c byte) {
	l.inWord = true
	l.text.WriteByte(c)
	l.prev = c
}

func (l *shellLexer) appendQuoted(content string) {
	l.inWord = true
	l.text.WriteString(content)
	l.prev = 0
}

func (l *shellLexer) endWord() {
	if !l.inWord {
		return
	}
	l.words = append(l.words, l.text.String())
	l.text.Reset()
	l.inWord, l.prev = false, 0
}

func (l *shellLexer) endStage(final bool) error {
	if len(l.words) == 0 {
		if final && len(l.stages) == 0 {
			return fmt.Errorf("command is empty")
		}
		return fmt.Errorf("pipeline stage is empty")
	}
	l.stages = append(l.stages, l.words)
	l.words = nil
	return nil
}

func isNameByte(c byte) bool {
	return c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
