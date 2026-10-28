package provider

import "testing"

func TestOnlyClaudeUsesTheUnprivilegedBubblewrapInDocker(t *testing.T) {
	for name, test := range map[string]struct {
		adapter Adapter
		want    bool
	}{
		"claude":   {NewClaude(), true},
		"codex":    {NewCodex(), false},
		"opencode": {NewOpenCode(), false},
	} {
		if got := UsesUnprivilegedBubblewrap(test.adapter); got != test.want {
			t.Fatalf("%s uses unprivileged bubblewrap = %v, want %v", name, got, test.want)
		}
	}
}
