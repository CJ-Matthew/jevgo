// Package jev is a Go client for TypeSafe's Jev, a System One model that
// answers typed questions instead of generating text.
//
// A question set is declared once, bound to the shape of state it evaluates,
// and validated when it is built rather than when traffic arrives:
//
//	type Dept string
//	const (
//		Billing   Dept = "billing"
//		Technical Dept = "technical"
//	)
//
//	var Department = jev.Choice[Dept]{
//		Name:         "department",
//		Instructions: "Which team should handle this",
//		Criteria: map[Dept]any{
//			Billing:   "Invoices, refunds, charges",
//			Technical: "Bugs, outages, API errors",
//		},
//	}
//
//	var TicketSet = jev.MustNewSet[Ticket](Department, Frustration, Urgent)
//
//	res, err := TicketSet.Eval(ctx, jev.New(), ticket)
//	dept, err := Department.From(res)
//	switch dept.Choice {
//	case Billing:
//		...
//	}
//
// The package reports what the model returned and stops there. It has no view
// on what confidence is high enough to act on, and no routing, thresholds or
// policy of its own: those depend on what being wrong costs you, so they are
// left to the caller.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	// DefaultBaseURL is TypeSafe's API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the stable alias TypeSafe recommends.
	DefaultModel = "jev-latest"
	// APIKeyEnv is the environment variable read when no key is passed.
	APIKeyEnv = "TYPESAFE_API_KEY"

	endpointPath = "/v1/systemone"
)

// Retry controls how failed requests are re-sent.
//
// Only 429, 529 and 5xx are retried; 401 and 422 are returned immediately
// because another attempt cannot change the outcome. A Retry-After header is
// always honoured in preference to the computed backoff.
type Retry struct {
	// MaxAttempts is the total number of attempts, not the number of
	// retries. Values below 1 are treated as 1.
	MaxAttempts int
	// Base is the first backoff interval; it doubles each attempt.
	Base time.Duration
	// Max caps a single backoff interval.
	Max time.Duration
}

// DefaultRetry is used when WithRetry is not passed.
var DefaultRetry = Retry{MaxAttempts: 3, Base: 200 * time.Millisecond, Max: 5 * time.Second}

// Client talks to the API. It is safe for concurrent use.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
	retry   Retry
}

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sets the credential, overriding TYPESAFE_API_KEY.
func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

// WithBaseURL points the client at a different API root.
func WithBaseURL(url string) Option { return func(c *Client) { c.baseURL = url } }

// WithModel selects a model version, e.g. "jev-1.13.0". Defaults to
// DefaultModel.
func WithModel(model string) Option { return func(c *Client) { c.model = model } }

// WithHTTPClient replaces the whole HTTP client, including its timeout.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithTransport replaces only the round-tripper, leaving timeouts alone.
//
// This is the seam for tests: everything above it, including retries and
// decoding, still runs. See the jevtest package.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.http.Transport = rt }
}

// WithRetry replaces the retry policy. A zero MaxAttempts disables retrying.
func WithRetry(r Retry) Option { return func(c *Client) { c.retry = r } }

// New builds a Client, reading TYPESAFE_API_KEY unless WithAPIKey is passed.
//
// A missing key is not reported here but on the first request, as ErrNoAPIKey.
func New(opts ...Option) *Client {
	c := &Client{
		apiKey:  os.Getenv(APIKeyEnv),
		baseURL: DefaultBaseURL,
		model:   DefaultModel,
		http:    &http.Client{Timeout: 30 * time.Second},
		retry:   DefaultRetry,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Model reports the model this client requests.
func (c *Client) Model() string { return c.model }

type apiRequest struct {
	Model     string                  `json:"model"`
	State     any                     `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
}

type apiResponse struct {
	Model   string               `json:"model"`
	Answers map[string]rawAnswer `json:"answers"`
	Usage   Usage                `json:"usage"`
}

// evaluate sends one request and decodes one result.
func (c *Client) evaluate(ctx context.Context, state any, qs map[string]wireQuestion) (*Result, error) {
	if c.apiKey == "" {
		return nil, ErrNoAPIKey
	}
	body, err := json.Marshal(apiRequest{Model: c.model, State: state, Questions: qs})
	if err != nil {
		return nil, fmt.Errorf("jev: encoding request: %w", err)
	}

	attempts := max(c.retry.MaxAttempts, 1)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		start := time.Now()
		res, err := c.attempt(ctx, body)
		if err == nil {
			res.Latency = time.Since(start)
			return res, nil
		}
		lastErr = err

		if attempt == attempts || !shouldRetry(err) {
			break
		}
		if err := sleep(ctx, c.backoff(attempt, err)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) attempt(ctx context.Context, body []byte) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpointPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev: sending request: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jev: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Message:    apiMessage(payload),
			RetryAfter: retryAfter(resp.Header),
		}
	}

	var decoded apiResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("jev: decoding response: %w", err)
	}
	return &Result{Model: decoded.Model, Usage: decoded.Usage, answers: decoded.Answers}, nil
}

// backoff is exponential with full jitter, unless the server said otherwise.
func (c *Client) backoff(attempt int, err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter
	}
	base := c.retry.Base
	if base <= 0 {
		base = DefaultRetry.Base
	}
	d := base << (attempt - 1)
	if limit := c.retry.Max; limit > 0 && d > limit {
		d = limit
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}

func shouldRetry(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.retryable()
	}
	// Transport-level failures are worth another go; a cancelled context is
	// not, and is caught by sleep.
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// apiMessage pulls a human-readable message out of an error body, falling back
// to the raw text when it is not the shape we expect.
func apiMessage(payload []byte) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(payload, &body); err == nil {
		for _, s := range []string{body.Error.Message, body.Message, body.Detail} {
			if s != "" {
				return s
			}
		}
	}
	const limit = 200
	if len(payload) > limit {
		return string(payload[:limit]) + "..."
	}
	return string(payload)
}

func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
