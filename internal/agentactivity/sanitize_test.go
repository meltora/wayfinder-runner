package agentactivity

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		want       string
		count      int
		categories []string
	}{
		{"plain", "Updated  the\nparser.", "Updated the parser.", 0, []string{}},
		{"bearer", "Header Bearer abcdefghijklmnop123 sent", "Header [redacted] sent", 1, []string{"credential"}},
		{"github token", "token ghp_abcdefghijklmnopqrstuvwxyz0123", "token [redacted]", 1, []string{"credential"}},
		{"assignment", "set API_KEY=supersecretvalue now", "set [redacted] now", 1, []string{"credential"}},
		{"credential url", "clone https://user:pass@example.com/repo.git ok", "clone [redacted] ok", 1, []string{"credential"}},
		{"plain url kept", "see https://example.com/docs/page ok", "see https://example.com/docs/page ok", 0, []string{}},
		{"email", "mail ops@example.com please", "mail [redacted] please", 1, []string{"personal_data"}},
		{"absolute path", "edited /home/omar/project/main.go and (~/notes/todo.md)", "edited [redacted] and ([redacted])", 2, []string{"filesystem_path"}},
		{"relative path kept", "edited src/app/main.go", "edited src/app/main.go", 0, []string{}},
		{"mixed", "ops@example.com wrote sk-abcdefghijklmnop to /etc/app/conf", "[redacted] wrote [redacted] to [redacted]", 3, []string{"credential", "filesystem_path", "personal_data"}},
		{"private key", "-----BEGIN RSA PRIVATE KEY----- data", "[redacted] data", 1, []string{"credential"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in, 280)
			if got.Message != tc.want || got.RedactionCount != tc.count || !reflect.DeepEqual(got.Categories, tc.categories) || got.Truncated {
				t.Fatalf("got %#v", got)
			}
		})
	}
}

func TestSanitizeTruncatesUTF8Safely(t *testing.T) {
	got := Sanitize(strings.Repeat("é", 100), 64)
	if !got.Truncated || len(got.Message) > 64 || !utf8.ValidString(got.Message) || !strings.HasSuffix(got.Message, "…") {
		t.Fatalf("got %q (%d bytes)", got.Message, len(got.Message))
	}
}

func TestSanitizeDropsControlCharactersAndInvalidUTF8(t *testing.T) {
	got := Sanitize("ok\x00\x07 \xff done", 280)
	if got.Message != "ok done" {
		t.Fatalf("got %q", got.Message)
	}
}

func TestSanitizeBlankInput(t *testing.T) {
	if got := Sanitize(" \n\t ", 280); got.Message != "" {
		t.Fatalf("got %q", got.Message)
	}
}
