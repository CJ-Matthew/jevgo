package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// DefaultConcurrency is used when WithConcurrency is not passed.
const DefaultConcurrency = 8

// MapResult is the outcome for one item. Exactly one of Result and Err is set.
type MapResult[S any] struct {
	// Index is the item's position in the input slice.
	Index int
	// Item is the input, so a result can be handled without carrying the
	// index back to the original slice.
	Item S
	// Result is the evaluation, or nil if Err is set.
	Result *Result
	// Err is why this item failed. Other items are unaffected.
	Err error
}

type mapConfig struct {
	concurrency int
	limits      Limits
}

// MapOption configures Map.
type MapOption func(*mapConfig)

// WithConcurrency caps in-flight requests. Defaults to DefaultConcurrency.
func WithConcurrency(n int) MapOption {
	return func(m *mapConfig) { m.concurrency = n }
}

// WithRateLimit sets the ceilings Map keeps under. Defaults to DefaultLimits,
// which mirrors TypeSafe's published limits. A zero field means unlimited.
func WithRateLimit(l Limits) MapOption {
	return func(m *mapConfig) { m.limits = l }
}

// Map evaluates a set against many items concurrently.
//
// Results come back in input order, one per item, each carrying its own error:
// a failure does not stop the others. The returned error is non-nil only when
// the whole run could not proceed, such as a cancelled context.
//
// Requests are kept under the configured limits. Token cost is only known
// after a response, so an estimate is reserved up front and reconciled with
// reported usage afterwards; the effect is to hold sustained throughput near
// the ceiling rather than to be exact about any single request.
func Map[S any](ctx context.Context, c *Client, set *Set[S], items []S, opts ...MapOption) ([]MapResult[S], error) {
	if c == nil {
		return nil, fmt.Errorf("jev: nil client")
	}
	if set == nil {
		return nil, fmt.Errorf("jev: nil set")
	}
	if len(items) == 0 {
		return nil, nil
	}

	cfg := mapConfig{concurrency: DefaultConcurrency, limits: DefaultLimits}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.concurrency < 1 {
		cfg.concurrency = 1
	}
	if cfg.concurrency > len(items) {
		cfg.concurrency = len(items)
	}

	lim := newLimiter(cfg.limits)
	questionTokens := estimateTokens(set.wire)

	out := make([]MapResult[S], len(items))
	idx := make(chan int)

	var wg sync.WaitGroup
	wg.Add(cfg.concurrency)
	for range cfg.concurrency {
		go func() {
			defer wg.Done()
			for i := range idx {
				out[i] = evalOne(ctx, c, set, items[i], i, lim, questionTokens)
			}
		}()
	}

feed:
	for i := range items {
		select {
		case <-ctx.Done():
			break feed
		case idx <- i:
		}
	}
	close(idx)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func evalOne[S any](ctx context.Context, c *Client, set *Set[S], item S, i int, lim *limiter, questionTokens int) MapResult[S] {
	res := MapResult[S]{Index: i, Item: item}

	est := questionTokens + estimateTokens(marshalState(item))
	if err := lim.wait(ctx, est); err != nil {
		res.Err = err
		return res
	}

	r, err := set.Eval(ctx, c, item)
	if err != nil {
		// The request still consumed capacity on the way out, so leave the
		// estimate standing rather than crediting it back.
		res.Err = err
		return res
	}
	lim.settle(est, r.Usage.InputTokens+r.Usage.OutputTokens)
	res.Result = r
	return res
}

// estimateTokens approximates token count from encoded size at roughly four
// bytes per token. It feeds the rate limiter only; nothing is billed on it.
func estimateTokens(v any) int {
	if s, ok := v.(string); ok {
		return len(s)/4 + 1
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 1
	}
	return len(b)/4 + 1
}
