package lambdaweb

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiter_allowsABurstThenTheSustainedRate(t *testing.T) {
	// Given: 100 requests per second, so a burst of 10
	l := newLimiter(100)
	now := time.Now().UnixNano()

	// When: 50 requests arrive in the same instant
	allowed := 0
	for range 50 {
		if l.allow(now) {
			allowed++
		}
	}

	// Then: only the burst passes
	if allowed != 10 {
		t.Errorf("allowed = %d, want 10", allowed)
	}
	// And: a tenth of a second later (10 more tokens) the next ten pass
	later := now + int64(100*time.Millisecond)
	allowed = 0
	for range 50 {
		if l.allow(later) {
			allowed++
		}
	}
	if allowed < 9 || allowed > 11 {
		t.Errorf("after 100ms allowed = %d, want about 10", allowed)
	}
}

func TestLimiter_nilAllowsEverything(t *testing.T) {
	var l *limiter
	for range 1000 {
		if !l.allow(0) {
			t.Fatal("a nil limiter refused a request")
		}
	}
}

func TestLimiter_isSafeUnderConcurrency(t *testing.T) {
	// Given: 1000 requests per second (a burst of 100) and 8 goroutines racing
	l := newLimiter(1000)
	now := time.Now().UnixNano()
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				if l.allow(now) {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// Then: exactly the burst got through, however the CAS loops interleaved
	if allowed.Load() != 100 {
		t.Errorf("allowed = %d, want 100", allowed.Load())
	}
}

func BenchmarkLimiterAllow(b *testing.B) {
	l := newLimiter(10000)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.allow(time.Now().UnixNano())
		}
	})
}
