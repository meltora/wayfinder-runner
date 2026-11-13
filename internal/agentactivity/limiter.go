package agentactivity

import "time"

const rateWindow = time.Minute

type Action int

const (
	Drop Action = iota
	Emit
	EmitTotalLimitNotice
	EmitRateLimitNotice
)

type Decision struct {
	Action   Action
	Sequence int
}

type Limiter struct {
	maxEvents       int
	maxPerMinute    int
	issued          int
	stopped         bool
	window          []time.Time
	lastRateNotice  time.Time
	rateNoticeGiven bool
}

func NewLimiter(maxEvents, maxPerMinute int) *Limiter {
	return &Limiter{maxEvents: maxEvents, maxPerMinute: maxPerMinute}
}

func (l *Limiter) Admit(now time.Time) Decision {
	if l.stopped {
		return Decision{Action: Drop}
	}
	if l.issued >= l.maxEvents-1 {
		l.stopped = true
		l.issued++
		return Decision{Action: EmitTotalLimitNotice, Sequence: l.issued}
	}
	l.prune(now)
	if len(l.window) >= l.maxPerMinute {
		if l.rateNoticeGiven && now.Sub(l.lastRateNotice) < rateWindow {
			return Decision{Action: Drop}
		}
		l.rateNoticeGiven = true
		l.lastRateNotice = now
		l.issued++
		return Decision{Action: EmitRateLimitNotice, Sequence: l.issued}
	}
	l.window = append(l.window, now)
	l.issued++
	return Decision{Action: Emit, Sequence: l.issued}
}

func (l *Limiter) prune(now time.Time) {
	kept := l.window[:0]
	for _, at := range l.window {
		if now.Sub(at) < rateWindow {
			kept = append(kept, at)
		}
	}
	l.window = kept
}
