// Package jevtest provides HTTP transports for testing code that uses jev
// without reaching the API.
//
// These replace the round-tripper rather than the client, so the real client
// still runs: retry policy, backoff, status handling, JSON decoding and typed
// answer extraction are all exercised against fixture bytes.
//
//	c := jev.New(
//		jev.WithAPIKey("test"),
//		jev.WithTransport(jevtest.Static(fixture)),
//	)
package jevtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
)

// RoundTripFunc adapts a function to http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Static answers every request with 200 and the given JSON body.
func Static(body string) http.RoundTripper {
	return Status(http.StatusOK, body)
}

// Status answers every request with the given status and body. Use it to
// exercise error paths: 401 and 422 are surfaced immediately, 429 and 529 are
// retried.
func Status(code int, body string) http.RoundTripper {
	return RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return respond(r, code, body, nil), nil
	})
}

// RetryAfter is Status with a Retry-After header, which the client honours in
// preference to its own backoff.
func RetryAfter(code int, seconds int, body string) http.RoundTripper {
	return RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{"Retry-After": []string{fmt.Sprint(seconds)}}
		return respond(r, code, body, h), nil
	})
}

// Sequence answers successive requests with successive transports, so a retry
// can be tested end to end:
//
//	jevtest.Sequence(
//		jevtest.Status(429, ""),
//		jevtest.Static(fixture),
//	)
//
// Requests beyond the last transport reuse it.
func Sequence(rts ...http.RoundTripper) http.RoundTripper {
	var (
		mu sync.Mutex
		i  int
	)
	return RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		if len(rts) == 0 {
			mu.Unlock()
			return nil, fmt.Errorf("jevtest: empty sequence")
		}
		rt := rts[min(i, len(rts)-1)]
		i++
		mu.Unlock()
		return rt.RoundTrip(r)
	})
}

// Fixture is one hand-written canned response.
//
// Files are JSON:
//
//	{
//	  "match": "Stripe",
//	  "status": 200,
//	  "response": {
//	    "model": "jev-1.13.0",
//	    "answers": { "...": { "type": "choice", "choice": "technical",
//	                          "probabilities": {"technical": 0.94},
//	                          "confidence": 0.94 } },
//	    "usage": {"input_tokens": 120, "output_tokens": 0}
//	  }
//	}
//
// Match is an optional substring of the request body; the first fixture whose
// Match appears wins, and a fixture with no Match matches anything. Status
// defaults to 200.
type Fixture struct {
	Match    string          `json:"match"`
	Status   int             `json:"status"`
	Response json.RawMessage `json:"response"`
}

// Replayer serves fixtures and records the requests it saw.
type Replayer struct {
	fixtures []Fixture

	mu       sync.Mutex
	requests []string
}

// Replay loads every *.json fixture in fsys, in filename order.
//
//	//go:embed testdata
//	var fixtures embed.FS
//	sub, _ := fs.Sub(fixtures, "testdata")
//	rt, err := jevtest.Replay(sub)
//
// It is an error for a fixture file to be malformed, so a typo fails the test
// that loads it rather than the assertion that depended on it.
func Replay(fsys fs.FS) (*Replayer, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, fmt.Errorf("jevtest: globbing fixtures: %w", err)
	}
	slices.Sort(names)

	r := &Replayer{}
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("jevtest: reading %s: %w", name, err)
		}
		var f Fixture
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("jevtest: parsing %s: %w", path.Base(name), err)
		}
		if len(f.Response) == 0 {
			return nil, fmt.Errorf("jevtest: %s: no response field", path.Base(name))
		}
		r.fixtures = append(r.fixtures, f)
	}
	if len(r.fixtures) == 0 {
		return nil, fmt.Errorf("jevtest: no *.json fixtures found")
	}
	return r, nil
}

// RoundTrip implements http.RoundTripper.
func (r *Replayer) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.requests = append(r.requests, body)
	r.mu.Unlock()

	for _, f := range r.fixtures {
		if f.Match != "" && !strings.Contains(body, f.Match) {
			continue
		}
		status := f.Status
		if status == 0 {
			status = http.StatusOK
		}
		return respond(req, status, string(f.Response), nil), nil
	}
	return nil, fmt.Errorf("jevtest: no fixture matched request body: %s", truncate(body, 200))
}

// Requests returns the bodies of every request served, in order, so a test can
// assert on what was actually sent.
func (r *Replayer) Requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.requests)
}

func respond(req *http.Request, code int, body string, h http.Header) *http.Response {
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func readBody(req *http.Request) (string, error) {
	if req.Body == nil {
		return "", nil
	}
	defer req.Body.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, req.Body); err != nil {
		return "", fmt.Errorf("jevtest: reading request body: %w", err)
	}
	return buf.String(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
