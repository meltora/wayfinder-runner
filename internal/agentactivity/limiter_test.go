package agentactivity

import (
	"testing"
	"time"
)

func TestLimiterReservesLastSlotForTotalNotice(t *testing.T) {
	start := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	limiter := NewLimiter(3, 60)
	decisions := []Decision{}
	for index := 0; index < 5; index++ {
		decisions = append(decisions, limiter.Admit(start.Add(time.Duration(index)*time.Second)))
	}
	want := []Decision{
		{Action: Emit, Sequence: 1},
		{Action: Emit, Sequence: 2},
		{Action: EmitTotalLimitNotice, Sequence: 3},
		{Action: Drop},
		{Action: Drop},
	}
	for index := range want {
		if decisions[index] != want[index] {
			t.Fatalf("decision %d=%#v want %#v", index, decisions[index], want[index])
		}
	}
}

func TestLimiterSingleEventBecomesNotice(t *testing.T) {
	limiter := NewLimiter(1, 60)
	now := time.Now()
	if got := limiter.Admit(now); got.Action != EmitTotalLimitNotice || got.Sequence != 1 {
		t.Fatalf("got %#v", got)
	}
	if got := limiter.Admit(now); got.Action != Drop {
		t.Fatalf("got %#v", got)
	}
}

func TestLimiterRateWindow(t *testing.T) {
	start := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	limiter := NewLimiter(100, 2)
	steps := []struct {
		offset time.Duration
		want   Decision
	}{
		{0, Decision{Action: Emit, Sequence: 1}},
		{time.Second, Decision{Action: Emit, Sequence: 2}},
		{2 * time.Second, Decision{Action: EmitRateLimitNotice, Sequence: 3}},
		{3 * time.Second, Decision{Action: Drop}},
		{4 * time.Second, Decision{Action: Drop}},
		{61 * time.Second, Decision{Action: Emit, Sequence: 4}},
		{62 * time.Second, Decision{Action: Emit, Sequence: 5}},
		{63 * time.Second, Decision{Action: EmitRateLimitNotice, Sequence: 6}},
		{64 * time.Second, Decision{Action: Drop}},
	}
	for index, step := range steps {
		if got := limiter.Admit(start.Add(step.offset)); got != step.want {
			t.Fatalf("step %d=%#v want %#v", index, got, step.want)
		}
	}
}

func TestLimiterTotalNoticeWinsOverRateNotice(t *testing.T) {
	start := time.Now()
	limiter := NewLimiter(3, 2)
	limiter.Admit(start)
	limiter.Admit(start)
	if got := limiter.Admit(start); got.Action != EmitTotalLimitNotice || got.Sequence != 3 {
		t.Fatalf("got %#v", got)
	}
}
