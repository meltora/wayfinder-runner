package daemon

import "strings"

var hfsIgnorableRunes = map[rune]bool{
	0x200c: true, 0x200d: true, 0x200e: true, 0x200f: true,
	0x202a: true, 0x202b: true, 0x202c: true, 0x202d: true, 0x202e: true,
	0x206a: true, 0x206b: true, 0x206c: true, 0x206d: true, 0x206e: true, 0x206f: true,
	0xfeff: true,
}

var gitmodulesShortNames = []string{"gitmod~1", "gitmod~2", "gitmod~3", "gitmod~4"}

func gitLookalikeName(name, reserved string, shortNames ...string) bool {
	if name == "" {
		return false
	}
	if strings.EqualFold(stripHFSIgnorable(name), reserved) {
		return true
	}
	lower := strings.ToLower(name)
	for _, prefix := range append([]string{reserved}, shortNames...) {
		if rest, ok := strings.CutPrefix(lower, prefix); ok && ntfsEquivalentTail(rest) {
			return true
		}
	}
	return false
}

func gitmodulesLookalike(name string) bool {
	if gitLookalikeName(name, ".gitmodules", gitmodulesShortNames...) {
		return true
	}
	lower := strings.ToLower(name)
	rest, ok := strings.CutPrefix(lower, "gi7eba~")
	if !ok || rest == "" || rest[0] < '1' || rest[0] > '9' {
		return false
	}
	return ntfsEquivalentTail(rest[1:])
}

func stripHFSIgnorable(name string) string {
	return strings.Map(func(r rune) rune {
		if hfsIgnorableRunes[r] {
			return -1
		}
		return r
	}, name)
}

func ntfsEquivalentTail(rest string) bool {
	trimmed := strings.TrimLeft(rest, ". ")
	return trimmed == "" || trimmed[0] == ':' || trimmed[0] == '\\'
}
