package jev

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for the statuses the TypeSafe API documents. Match them with
// errors.Is; reach for *APIError with errors.As when you need the detail.
var (
	// ErrNoAPIKey is returned before any request is made when no credential is
	// configured. Set TYPESAFE_API_KEY or pass WithAPIKey.
	ErrNoAPIKey = errors.New("jev: no API key configured")

	ErrAuth       = errors.New("jev: invalid or missing api key") // 401
	ErrValidation = errors.New("jev: request failed validation")  // 422
	ErrRateLimit  = errors.New("jev: rate limited")               // 429
	ErrOverloaded = errors.New("jev: service overloaded")         // 529
)

// APIError is a non-2xx response from the API.
//
// RetryAfter carries the Retry-After header when the server sent one. Retrying
// is handled for you on 429 and 529; see Retry.
type APIError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("jev: http %d", e.StatusCode)
	}
	return fmt.Sprintf("jev: http %d: %s", e.StatusCode, e.Message)
}

// Unwrap maps documented statuses onto sentinels so callers can branch with
// errors.Is without caring about numbers.
func (e *APIError) Unwrap() error {
	switch e.StatusCode {
	case 401:
		return ErrAuth
	case 422:
		return ErrValidation
	case 429:
		return ErrRateLimit
	case 529:
		return ErrOverloaded
	}
	return nil
}

// retryable reports whether another attempt could plausibly succeed. 401 and
// 422 never will, so they are surfaced immediately.
func (e *APIError) retryable() bool {
	switch e.StatusCode {
	case 429, 529:
		return true
	}
	return e.StatusCode >= 500
}

// AnswerError means the response did not contain a usable answer for a
// question, or contained one of the wrong shape.
type AnswerError struct {
	Question string
	Reason   string
}

func (e *AnswerError) Error() string {
	return fmt.Sprintf("jev: question %q: %s", e.Question, e.Reason)
}
