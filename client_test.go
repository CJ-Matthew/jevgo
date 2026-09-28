package jev

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CJ-Matthew/jevgo/jevtest"
)

const technicalFixture = `{
  "model": "jev-1.13.0",
  "answers": {
    "department": {"type":"choice","choice":"technical",
                   "probabilities":{"technical":0.94,"billing":0.04,"sales":0.02},
                   "confidence":0.91},
    "frustration": {"type":"score","score":1.4,
                    "probabilities":{"0":0.10,"1":0.50,"2":0.40},
                    "legend":{"0":"Calm","1":"Frustrated but civil","2":"Very angry"},
                    "confidence":0.62},
    "is_urgent": {"type":"noul","noul":0.93}
  },
  "usage": {"input_tokens":120,"output_tokens":0}
}`

var testSet = MustNewSet[Ticket](department, frustration, urgent)

func testClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	return New(WithAPIKey("test-key"), WithTransport(rt),
		WithRetry(Retry{MaxAttempts: 3, Base: time.Millisecond, Max: 2 * time.Millisecond}))
}

func TestEvalTypedAnswers(t *testing.T) {
	c := testClient(t, jevtest.Static(technicalFixture))
	res, err := testSet.Eval(context.Background(), c, Ticket{Body: "stripe is down", Plan: "growth"})
	if err != nil {
		t.Fatal(err)
	}

	if res.Model != "jev-1.13.0" {
		t.Errorf("model = %q", res.Model)
	}
	if res.Usage.InputTokens != 120 {
		t.Errorf("input tokens = %d", res.Usage.InputTokens)
	}
	if res.Latency <= 0 {
		t.Error("latency was not measured")
	}

	dept, err := department.From(res)
	if err != nil {
		t.Fatal(err)
	}
	// The whole point: this is a Dept, not a string.
	if dept.Choice != Technical {
		t.Errorf("choice = %v, want %v", dept.Choice, Technical)
	}
	if dept.Probabilities[Technical] != 0.94 {
		t.Errorf("probabilities = %v", dept.Probabilities)
	}
	if !dept.Confident(0.9) || dept.Confident(0.95) {
		t.Errorf("confidence %v gated wrongly", dept.Confidence)
	}

	fr, err := frustration.From(res)
	if err != nil {
		t.Fatal(err)
	}
	if fr.Score != 1.4 {
		t.Errorf("score = %v", fr.Score)
	}
	if fr.Level != 1 {
		t.Errorf("level = %d, want 1 (1.4 rounds down)", fr.Level)
	}
	if fr.Legend[2] != "Very angry" {
		t.Errorf("legend = %v", fr.Legend)
	}
	if fr.Probabilities[1] != 0.50 {
		t.Errorf("probabilities = %v", fr.Probabilities)
	}

	urg, err := urgent.From(res)
	if err != nil {
		t.Fatal(err)
	}
	if urg.Prob != 0.93 {
		t.Errorf("prob = %v", urg.Prob)
	}
}

func TestNoulPredicates(t *testing.T) {
	tests := []struct {
		prob      float64
		wantTrue  bool
		wantValue bool
		wantOK    bool
	}{
		{0.93, true, true, true},   // decisively yes
		{0.04, false, false, true}, // decisively no
		{0.52, true, false, false}, // technically true, but the model is shrugging
		{0.50, false, false, false},
		{0.90, true, true, true}, // exactly at the boundary counts
	}
	for _, tc := range tests {
		a := NoulAnswer{Prob: tc.prob}
		if got := a.True(); got != tc.wantTrue {
			t.Errorf("prob %v: True() = %v, want %v", tc.prob, got, tc.wantTrue)
		}
		v, ok := a.Decisive(0.1)
		if v != tc.wantValue || ok != tc.wantOK {
			t.Errorf("prob %v: Decisive(0.1) = (%v,%v), want (%v,%v)",
				tc.prob, v, ok, tc.wantValue, tc.wantOK)
		}
	}

	a := NoulAnswer{Prob: 0.7}
	if !a.TrueAbove(0.6) || a.TrueAbove(0.8) {
		t.Error("TrueAbove gated wrongly")
	}
}

func TestChoiceRejectsUndeclaredOption(t *testing.T) {
	const rogue = `{"model":"m","answers":{"department":
		{"type":"choice","choice":"legal","probabilities":{"legal":1},"confidence":1}},
		"usage":{}}`
	c := testClient(t, jevtest.Static(rogue))
	res, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := department.From(res); err == nil {
		t.Fatal("expected an error for an option that was never offered")
	} else if !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWrongAnswerTypeIsReported(t *testing.T) {
	const swapped = `{"model":"m","answers":{"department":{"type":"noul","noul":0.5}},"usage":{}}`
	c := testClient(t, jevtest.Static(swapped))
	res, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = department.From(res)
	var ansErr *AnswerError
	if !errors.As(err, &ansErr) {
		t.Fatalf("expected an AnswerError, got %v", err)
	}
	if !strings.Contains(ansErr.Reason, "expected a choice answer") {
		t.Fatalf("unexpected reason: %q", ansErr.Reason)
	}
}

func TestMissingAnswerIsReported(t *testing.T) {
	c := testClient(t, jevtest.Static(`{"model":"m","answers":{},"usage":{}}`))
	res, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := department.From(res); err == nil {
		t.Fatal("expected an error for a missing answer")
	}
}

func TestNoAPIKey(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	c := New(WithTransport(jevtest.Static(technicalFixture)))
	_, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"})
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("expected ErrNoAPIKey, got %v", err)
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	rt := jevtest.Sequence(
		jevtest.Status(429, `{"error":{"message":"slow down"}}`),
		jevtest.Static(technicalFixture),
	)
	c := testClient(t, rt)
	res, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"})
	if err != nil {
		t.Fatalf("expected the retry to succeed, got %v", err)
	}
	if res.Model != "jev-1.13.0" {
		t.Errorf("model = %q", res.Model)
	}
}

func TestDoesNotRetryAuthOrValidation(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{
		{401, ErrAuth},
		{422, ErrValidation},
	} {
		calls := 0
		rt := jevtest.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return jevtest.Status(tc.code, `{"error":{"message":"nope"}}`).RoundTrip(r)
		})
		c := testClient(t, rt)
		_, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"})
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: got %v, want %v", tc.code, err, tc.want)
		}
		if calls != 1 {
			t.Errorf("status %d: made %d calls, want 1 (never retried)", tc.code, calls)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.code {
			t.Errorf("status %d: APIError not recoverable from %v", tc.code, err)
		}
		if apiErr.Message != "nope" {
			t.Errorf("message = %q, want the decoded API message", apiErr.Message)
		}
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	rt := jevtest.Sequence(
		jevtest.RetryAfter(529, 0, ""),
		jevtest.Static(technicalFixture),
	)
	c := testClient(t, rt)
	if _, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	calls := 0
	rt := jevtest.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jevtest.Status(529, "").RoundTrip(r)
	})
	c := testClient(t, rt)
	_, err := testSet.Eval(context.Background(), c, Ticket{Body: "x"})
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("got %v, want ErrOverloaded", err)
	}
	if calls != 3 {
		t.Fatalf("made %d attempts, want 3", calls)
	}
}

func TestRequestShape(t *testing.T) {
	rep, err := jevtest.Replay(os.DirFS("testdata"))
	if err != nil {
		t.Fatal(err)
	}
	c := testClient(t, rep)
	if _, err := testSet.Eval(context.Background(), c, Ticket{Body: "Stripe keeps failing", Plan: "growth"}); err != nil {
		t.Fatal(err)
	}

	reqs := rep.Requests()
	if len(reqs) != 1 {
		t.Fatalf("saw %d requests, want 1", len(reqs))
	}
	body := reqs[0]
	for _, want := range []string{
		`"model":"jev-latest"`,
		`"department"`, `"frustration"`, `"is_urgent"`,
		`"type":"choice"`, `"type":"score"`, `"type":"noul"`,
		`"Stripe keeps failing"`,
		`"plan":"growth"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s\nbody: %s", want, body)
		}
	}
}

func TestStringStateIsSentAsAString(t *testing.T) {
	rep, err := jevtest.Replay(os.DirFS("testdata"))
	if err != nil {
		t.Fatal(err)
	}
	set := MustNewSet[string](department, frustration, urgent)
	c := testClient(t, rep)
	if _, err := set.Eval(context.Background(), c, "Stripe is down"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Requests()[0], `"state":"Stripe is down"`) {
		t.Errorf("state was not sent as a bare string: %s", rep.Requests()[0])
	}
}

func TestReplayFallsThroughToUnmatchedFixture(t *testing.T) {
	rep, err := jevtest.Replay(os.DirFS("testdata"))
	if err != nil {
		t.Fatal(err)
	}
	c := testClient(t, rep)
	res, err := testSet.Eval(context.Background(), c, Ticket{Body: "what does enterprise cost"})
	if err != nil {
		t.Fatal(err)
	}
	dept, err := department.From(res)
	if err != nil {
		t.Fatal(err)
	}
	if dept.Choice != Sales {
		t.Errorf("choice = %v, want %v from the default fixture", dept.Choice, Sales)
	}
	if dept.Confident(0.5) {
		t.Error("0.33 confidence should not clear a 0.5 threshold")
	}
}
