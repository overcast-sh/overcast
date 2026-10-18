package lambdaweb

import (
	"sync/atomic"
	"time"
)

// limiter is a lock-free rate limiter (the generic cell rate algorithm, GCRA):
// one atomic holds the "theoretical arrival time" of the next conforming
// request. It allows a short burst, as AWS does ("brief bursts above the limit
// might be accepted"), and sustains rate requests per second.
//
// A nil *limiter allows everything.
type limiter struct {
	interval int64 // nanoseconds between conforming requests
	burst    int64 // how far ahead of now the schedule may run, in nanoseconds
	tat      atomic.Int64
}

// burstFraction is the share of one second of traffic a limiter lets through
// at once: rate/10 requests, at least one.
const burstFraction = 10

// newLimiter returns a limiter for rate requests per second, or nil for a rate
// that means "unlimited" (rate < 0). A rate of 0 blocks everything and is the
// caller's business (EndpointDisabled), not the limiter's.
func newLimiter(rate int) *limiter {
	if rate <= 0 {
		return nil
	}
	interval := int64(time.Second) / int64(rate)
	if interval < 1 {
		interval = 1
	}
	burst := max(int64(rate)/burstFraction, 1) * interval
	return &limiter{interval: interval, burst: burst}
}

// allow reports whether a request arriving at now (Unix nanoseconds) conforms.
func (l *limiter) allow(now int64) bool {
	if l == nil {
		return true
	}
	for {
		tat := l.tat.Load()
		base := max(tat, now)
		next := base + l.interval
		if next-now > l.burst {
			return false
		}
		if l.tat.CompareAndSwap(tat, next) {
			return true
		}
	}
}
