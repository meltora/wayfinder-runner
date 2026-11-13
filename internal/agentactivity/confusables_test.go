package agentactivity

import (
	"reflect"
	"testing"
)

func TestSanitizeRedactsHomoglyphKeyNamesAndPrefixes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"cyrillic a in api_key", "set аpi_key=hunter2xyz now", "set [redacted] now"},
		{"cyrillic a in password", "pаssword: hunter2xyz", "[redacted]"},
		{"greek omicron in token", "tοken=hunter2xyz", "[redacted]"},
		{"uppercase cyrillic in secret", "SЕCRET=hunter2xyz", "[redacted]"},
		{"cyrillic k in anthropic prefix", "key sк-ant-api03-abcdefghijklmnop", "key [redacted]"},
		{"cyrillic o in github prefix", "tok ghо_abcdefghijklmnopqrstuvwxyz0123", "tok [redacted]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in, 400)
			if got.Message != tc.want {
				t.Fatalf("message = %q, want %q", got.Message, tc.want)
			}
			if !reflect.DeepEqual(got.Categories, []string{"credential"}) {
				t.Fatalf("categories = %v", got.Categories)
			}
		})
	}
}

func TestSanitizeKeepsOrdinaryNonLatinText(t *testing.T) {
	for _, text := range []string{"Обновил парсер и тесты.", "Ενημέρωσα τον αναλυτή.", "Ažurirao sam parser."} {
		got := Sanitize(text, 400)
		if got.Message != text || got.RedactionCount != 0 {
			t.Fatalf("Sanitize(%q) = %q (%d redactions)", text, got.Message, got.RedactionCount)
		}
	}
}

func TestSanitizeRedactsAbsolutePathsContainingSpaces(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{"one space", "read /home/omar/my dir/secret.txt now", "read [redacted] now", 1},
		{"two spaced segments", "open /Users/jo/My Documents/Client Files/plan.md please", "open [redacted] please", 1},
		{"windows path", `saved C:\Users\jo\My Files\notes.txt ok`, "saved [redacted] ok", 1},
		{"following words kept", "edited /etc/app/conf and the tests", "edited [redacted] and the tests", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in, 400)
			if got.Message != tc.want || got.RedactionCount != tc.count {
				t.Fatalf("Sanitize(%q) = %q (%d), want %q (%d)", tc.in, got.Message, got.RedactionCount, tc.want, tc.count)
			}
		})
	}
}
