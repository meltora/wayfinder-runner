package daemon

import (
	"strings"
	"testing"
)

func TestUtilityBoundaries(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Fatalf("truncate() = %q, want %q", got, "abc…")
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Fatalf("truncate exact length = %q, want %q", got, "abc")
	}
	if got := truncate("abc", 0); got != "" {
		t.Fatalf("truncate zero length = %q, want empty", got)
	}
	if got := truncate(strings.Repeat("x", 8), 4); got != "xxxx…" {
		t.Fatalf("truncate repeated string = %q", got)
	}
	if now := timeNowUnixNano(); now <= 0 {
		t.Fatalf("timeNowUnixNano() = %d, want positive timestamp", now)
	}
}
