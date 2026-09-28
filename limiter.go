package jev

import (
	"context"
	"sync"
	"time"
)

// Limits are the API's documented rate limits. Zero means unlimited.
//
// TypeSafe documents 1,200 requests per minute and 250,000 tokens per second.
type Limits struct {
	RequestsPerMinute int
	TokensPerSecond   int
}

// DefaultLimits mirrors TypeSafe's published limits.
var DefaultLimits = Limits{RequestsPerMinute: 1200, TokensPerSecond: 250_000}

// limiter is a pair of token buckets, one for requests and one for tokens.
//
// Token cost is not known until the response reports it, so a request reserves
// an estimate from the bucket and the difference is settled afterwards. The
// estimate is deliberately crude; it exists to keep sustained throughput under
// the published ceiling, not to be exact about any single request.
type limiter struct {
	mu sync.Mutex

	reqRate  float64 // per second
	reqBurst float64
	reqAvail float64

	tokRate  float64
	tokBurst float64
	tokAvail float64

	last time.Time
	now  func() time.Time // swapped in tests
}

func newLimiter(l Limits) *limiter {
	lim := &limiter{now: time.Now}
	if l.RequestsPerMinute > 0 {
		lim.reqRate = float64(l.RequestsPerMinute) / 60
		lim.reqBurst = float64(l.RequestsPerMinute) / 60
		lim.reqAvail = lim.reqBurst
	}
	if l.TokensPerSecond > 0 {
		lim.tokRate = float64(l.TokensPerSecond)
		lim.tokBurst = float64(l.TokensPerSecond)
		lim.tokAvail = lim.tokBurst
	}
	lim.last = lim.now()
	return lim
}

// enabled reports whether either bucket is active.
func (l *limiter) enabled() bool { return l != nil && (l.reqRate > 0 || l.tokRate > 0) }

// wait blocks until one request carrying estTokens may proceed.
func (l *limiter) wait(ctx context.Context, estTokens int) error {
	if !l.enabled() {
		return nil
	}
	for {
		d := l.reserve(float64(estTokens))
		if d <= 0 {
			return nil
		}
		if err := sleep(ctx, d); err != nil {
			return err
		}
	}
}

// reserve takes capacity if available, otherwise reports how long to wait.
func (l *limiter) reserve(tokens float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()

	var wait time.Duration
	if l.reqRate > 0 && l.reqAvail < 1 {
		wait = durationFor((1-l.reqAvail)/l.reqRate, wait)
	}
	// A request larger than the whole bucket would wait forever, so it is
	// clamped to the burst size and allowed through once the bucket is full.
	want := min(tokens, l.tokBurst)
	if l.tokRate > 0 && l.tokAvail < want {
		wait = durationFor((want-l.tokAvail)/l.tokRate, wait)
	}
	if wait > 0 {
		return wait
	}
	if l.reqRate > 0 {
		l.reqAvail--
	}
	if l.tokRate > 0 {
		l.tokAvail -= want
	}
	return 0
}

// settle corrects the token bucket once real usage is known.
func (l *limiter) settle(estimated, actual int) {
	if l == nil || l.tokRate <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	l.tokAvail -= float64(actual - estimated)
	if l.tokAvail < -l.tokBurst {
		l.tokAvail = -l.tokBurst
	}
	if l.tokAvail > l.tokBurst {
		l.tokAvail = l.tokBurst
	}
}

func (l *limiter) refill() {
	now := l.now()
	elapsed := now.Sub(l.last).Seconds()
	if elapsed <= 0 {
		return
	}
	l.last = now
	if l.reqRate > 0 {
		l.reqAvail = min(l.reqBurst, l.reqAvail+elapsed*l.reqRate)
	}
	if l.tokRate > 0 {
		l.tokAvail = min(l.tokBurst, l.tokAvail+elapsed*l.tokRate)
	}
}

func durationFor(seconds float64, atLeast time.Duration) time.Duration {
	d := time.Duration(seconds * float64(time.Second))
	if d < time.Millisecond {
		d = time.Millisecond
	}
	return max(d, atLeast)
}
