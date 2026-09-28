package jev

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CJ-Matthew/jevgo/jevtest"
)

// choiceFor builds a response picking dept, so each item can be told apart.
func choiceFor(dept Dept) string {
	return fmt.Sprintf(`{"model":"jev-1.13.0","answers":{"department":
		{"type":"choice","choice":%q,"probabilities":{%q:1},"confidence":1}},
		"usage":{"input_tokens":10,"output_tokens":0}}`, dept, dept)
}

var deptOnly = MustNewSet[Ticket](department)

func TestMapPreservesOrder(t *testing.T) {
	// Answer according to the ticket body, with a delay that is longest for
	// the first item, so completion order cannot match input order.
	rt := jevtest.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := readAll(r)
		switch {
		// Match on the whole phrase: the request also carries the question
		// criteria, so a bare word like "outage" would match every body.
		case strings.Contains(body, "invoice is wrong"):
			time.Sleep(30 * time.Millisecond)
			return jevtest.Static(choiceFor(Billing)).RoundTrip(r)
		case strings.Contains(body, "servers are down"):
			return jevtest.Static(choiceFor(Technical)).RoundTrip(r)
		default:
			time.Sleep(10 * time.Millisecond)
			return jevtest.Static(choiceFor(Sales)).RoundTrip(r)
		}
	})

	items := []Ticket{{Body: "invoice is wrong"}, {Body: "servers are down"}, {Body: "pricing please"}}
	res, err := Map(context.Background(), testClient(t, rt), deptOnly, items,
		WithConcurrency(3), WithRateLimit(Limits{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}

	want := []Dept{Billing, Technical, Sales}
	for i, r := range res {
		if r.Err != nil {
			t.Fatalf("item %d: %v", i, r.Err)
		}
		if r.Index != i {
			t.Errorf("item %d: Index = %d", i, r.Index)
		}
		if r.Item.Body != items[i].Body {
			t.Errorf("item %d: Item = %q", i, r.Item.Body)
		}
		got, err := department.From(r.Result)
		if err != nil {
			t.Fatal(err)
		}
		if got.Choice != want[i] {
			t.Errorf("item %d: choice = %v, want %v", i, got.Choice, want[i])
		}
	}
}

func TestMapIsolatesPerItemErrors(t *testing.T) {
	rt := jevtest.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := readAll(r)
		if strings.Contains(body, "poison") {
			return jevtest.Status(422, `{"error":{"message":"bad question"}}`).RoundTrip(r)
		}
		return jevtest.Static(choiceFor(Billing)).RoundTrip(r)
	})

	items := []Ticket{{Body: "fine"}, {Body: "poison"}, {Body: "also fine"}}
	res, err := Map(context.Background(), testClient(t, rt), deptOnly, items,
		WithRateLimit(Limits{}))
	if err != nil {
		t.Fatalf("a failing item must not fail the whole run: %v", err)
	}

	if res[0].Err != nil || res[2].Err != nil {
		t.Errorf("healthy items were affected: %v, %v", res[0].Err, res[2].Err)
	}
	if !errors.Is(res[1].Err, ErrValidation) {
		t.Errorf("item 1 error = %v, want ErrValidation", res[1].Err)
	}
	if res[1].Result != nil {
		t.Error("a failed item should carry no result")
	}
}

func TestMapRespectsConcurrencyLimit(t *testing.T) {
	var inFlight, peak atomic.Int64
	rt := jevtest.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return jevtest.Static(choiceFor(Billing)).RoundTrip(r)
	})

	items := make([]Ticket, 20)
	for i := range items {
		items[i] = Ticket{Body: fmt.Sprintf("ticket %d", i)}
	}
	if _, err := Map(context.Background(), testClient(t, rt), deptOnly, items,
		WithConcurrency(4), WithRateLimit(Limits{})); err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got > 4 {
		t.Errorf("peak concurrency was %d, want at most 4", got)
	}
}

func TestMapStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	rt := jevtest.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 2 {
			cancel()
		}
		time.Sleep(2 * time.Millisecond)
		return jevtest.Static(choiceFor(Billing)).RoundTrip(r)
	})

	items := make([]Ticket, 50)
	_, err := Map(ctx, testClient(t, rt), deptOnly, items, WithConcurrency(1), WithRateLimit(Limits{}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if got := calls.Load(); got >= int64(len(items)) {
		t.Errorf("made %d calls; cancellation should have cut the run short", got)
	}
}

func TestMapEmptyInput(t *testing.T) {
	res, err := Map(context.Background(), testClient(t, jevtest.Static(technicalFixture)),
		deptOnly, []Ticket{})
	if err != nil || res != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", res, err)
	}
}

func TestLimiterSpacesRequests(t *testing.T) {
	// 60 requests per minute is one per second.
	l := newLimiter(Limits{RequestsPerMinute: 60})
	now := time.Now()
	l.now = func() time.Time { return now }
	l.last = now
	l.reqAvail = 1

	if d := l.reserve(0); d != 0 {
		t.Fatalf("first request waited %v, want 0", d)
	}
	if d := l.reserve(0); d <= 0 {
		t.Fatal("second immediate request should have been asked to wait")
	}

	now = now.Add(time.Second)
	if d := l.reserve(0); d != 0 {
		t.Fatalf("after a second the bucket should have refilled, waited %v", d)
	}
}

func TestLimiterAccountsForTokens(t *testing.T) {
	l := newLimiter(Limits{TokensPerSecond: 1000})
	now := time.Now()
	l.now = func() time.Time { return now }
	l.last = now

	if d := l.reserve(800); d != 0 {
		t.Fatalf("800 of 1000 tokens waited %v, want 0", d)
	}
	if d := l.reserve(800); d <= 0 {
		t.Fatal("a second 800-token request should have been asked to wait")
	}

	// Settling with higher real usage than estimated costs more capacity.
	l.settle(100, 500)
	if l.tokAvail >= 200 {
		t.Errorf("tokAvail = %v; under-estimating should have cost capacity", l.tokAvail)
	}
}

func TestLimiterDisabledWhenZero(t *testing.T) {
	l := newLimiter(Limits{})
	if l.enabled() {
		t.Fatal("zero limits should disable the limiter")
	}
	if err := l.wait(context.Background(), 1_000_000); err != nil {
		t.Fatal(err)
	}
}

// readAll copies a request body without consuming it for the caller.
func readAll(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Body = &replayBody{s: sb.String()}
	return sb.String(), nil
}

type replayBody struct {
	s string
	i int
	sync.Mutex
}

func (b *replayBody) Read(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	if b.i >= len(b.s) {
		return 0, fmt.Errorf("EOF")
	}
	n := copy(p, b.s[b.i:])
	b.i += n
	return n, nil
}
func (b *replayBody) Close() error { return nil }
