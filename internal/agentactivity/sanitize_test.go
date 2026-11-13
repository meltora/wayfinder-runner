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
		{"quoted json key", `config "api_key": "sk-abcdefghijklmnop" set`, "config [redacted] set", 1, []string{"credential"}},
		{"single quoted value", "export token='abc def ghi' done", "export [redacted] done", 1, []string{"credential"}},
		{"jwt", "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U ok", "jwt [redacted] ok", 1, []string{"credential"}},
		{"anthropic key", "key sk-ant-api03-abcdefghijklmnop", "key [redacted]", 1, []string{"credential"}},
		{"github server token", "tok ghs_abcdefghijklmnopqrstuvwxyz0123", "tok [redacted]", 1, []string{"credential"}},
		{"slack token", "slack xoxs-1234567890-abcdef", "slack [redacted]", 1, []string{"credential"}},
		{"aws session key", "aws ASIAABCDEFGHIJKLMNOP", "aws [redacted]", 1, []string{"credential"}},
		{"high entropy run", "blob Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MEFCQ0RFRg== end", "blob [redacted] end", 1, []string{"credential"}},
		{"long lowercase word kept", "see implementation_details_of_the_parser_module ok", "see implementation_details_of_the_parser_module ok", 0, []string{}},
		{"backtick path", "opened `/home/dev/repo/main.go`", "opened `[redacted]`", 1, []string{"filesystem_path"}},
		{"angle path", "file </var/lib/app/data>", "file <[redacted]>", 1, []string{"filesystem_path"}},
		{"windows backslash path", `edited C:\Users\dev\repo\main.go now`, "edited [redacted] now", 1, []string{"filesystem_path"}},
		{"windows slash path", "edited C:/Users/dev/main.go now", "edited [redacted] now", 1, []string{"filesystem_path"}},
		{"home tilde short", "see ~/notes for more", "see [redacted] for more", 1, []string{"filesystem_path"}},
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

func TestSanitizeDropsMessagesContainingPEMBlocks(t *testing.T) {
	for _, in := range []string{
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAabc\n-----END RSA PRIVATE KEY-----",
		"here is -----BEGIN OPENSSH PRIVATE KEY----- b3BlbnNzaC1rZXktdjEAAAAA",
		"cert -----BEGIN CERTIFICATE----- MIIB",
	} {
		if got := Sanitize(in, 280); got.Message != "" {
			t.Fatalf("PEM content forwarded: %q", got.Message)
		}
	}
}

func TestSanitizeRedactsKnownSecrets(t *testing.T) {
	secret := "v1.installation-token-value"
	got := Sanitize("pushing with "+secret+" now", 280, secret, "")
	if strings.Contains(got.Message, secret) || got.Message != "pushing with [redacted] now" ||
		got.RedactionCount != 1 || !reflect.DeepEqual(got.Categories, []string{"credential"}) {
		t.Fatalf("got %#v", got)
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
