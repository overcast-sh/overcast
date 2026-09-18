package stepfunctions

import (
	"testing"
	"time"
)

func TestRetryDelay_fullJitterScalesTheCappedBackoff(t *testing.T) {
	// Given: a FULL-jitter retrier with a MaxDelaySeconds cap and a pinned jitter
	interval, rate, maxDelay := 2.0, 3.0, 10.0
	retrier := aslRetrier{IntervalSeconds: &interval, BackoffRate: &rate, MaxDelaySeconds: &maxDelay, JitterStrategy: "FULL"}
	saved := retryJitter
	retryJitter = func() float64 { return 0.5 }
	defer func() { retryJitter = saved }()

	// When: the third attempt's delay is computed (2 × 3² = 18, capped at 10)
	got := retryDelay(retrier, 2)

	// Then: the cap applies first and the jitter scales it
	if want := 5 * time.Second; got != want {
		t.Errorf("delay = %s, want %s", got, want)
	}
}

func TestRetryDelay_noJitterByDefault(t *testing.T) {
	// Given: a retrier with the default JitterStrategy
	retrier := aslRetrier{}

	// When: the second attempt's delay is computed
	got := retryDelay(retrier, 1)

	// Then: it is the plain exponential backoff (1 × 2¹)
	if want := 2 * time.Second; got != want {
		t.Errorf("delay = %s, want %s", got, want)
	}
}

func TestErrorMatches_statesTimeoutMatchesHeartbeatTimeout(t *testing.T) {
	// Given: a heartbeat timeout
	serr := &stateError{name: errHeartbeatTimeout}

	// When / Then: States.Timeout matches it, States.HeartbeatTimeout matches it
	if !errorMatches([]string{errTimeout}, serr) {
		t.Error("States.Timeout did not match States.HeartbeatTimeout")
	}
	if !errorMatches([]string{errHeartbeatTimeout}, serr) {
		t.Error("States.HeartbeatTimeout did not match itself")
	}
}
