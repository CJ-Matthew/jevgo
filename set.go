package jev

import (
	"context"
	"fmt"
)

// Set is a group of questions evaluated together in one request.
//
// S is the shape of state the set evaluates, so a set written for tickets
// cannot be pointed at a resume. State is sent as JSON unless S is a string,
// which is sent as-is.
//
// Questions in a set are answered independently and in parallel, so adding one
// costs very little. That makes it worth asking everything up front, including
// questions only some branches need, and discarding the answers you do not
// use, rather than making a second call to find out.
type Set[S any] struct {
	questions []Question
	wire      map[string]wireQuestion
}

// NewSet builds a Set, returning an error if any question is malformed or two
// share a Name. Use it when questions are assembled at runtime; for the usual
// package-level set, use MustNewSet.
func NewSet[S any](questions ...Question) (*Set[S], error) {
	if len(questions) == 0 {
		return nil, fmt.Errorf("jev: set has no questions")
	}
	w := make(map[string]wireQuestion, len(questions))
	for _, q := range questions {
		if err := q.validate(); err != nil {
			return nil, fmt.Errorf("jev: %w", err)
		}
		n := q.name()
		if _, dup := w[n]; dup {
			// Names are map keys on the wire, so a duplicate would
			// silently drop one question and its answer.
			return nil, fmt.Errorf("jev: duplicate question name %q", n)
		}
		wq, err := q.wire()
		if err != nil {
			return nil, fmt.Errorf("jev: question %q: %w", n, err)
		}
		w[n] = wq
	}
	return &Set[S]{questions: questions, wire: w}, nil
}

// MustNewSet is NewSet, panicking on a malformed set.
//
// A bad set is a programming mistake that cannot depend on runtime input, so
// this fails at package initialisation rather than on the first request:
//
//	var TicketSet = jev.MustNewSet[Ticket](Department, Frustration, Urgent)
func MustNewSet[S any](questions ...Question) *Set[S] {
	s, err := NewSet[S](questions...)
	if err != nil {
		panic(err)
	}
	return s
}

// Len reports how many questions the set asks.
func (s *Set[S]) Len() int { return len(s.questions) }

// Names lists the question names in declaration order.
func (s *Set[S]) Names() []string {
	out := make([]string, 0, len(s.questions))
	for _, q := range s.questions {
		out = append(out, q.name())
	}
	return out
}

// Eval sends the set against one piece of state.
//
// Read answers back through the questions themselves, which is what makes them
// typed: Department.From(res) returns a ChoiceAnswer[Dept].
func (s *Set[S]) Eval(ctx context.Context, c *Client, state S) (*Result, error) {
	if c == nil {
		return nil, fmt.Errorf("jev: nil client")
	}
	return c.evaluate(ctx, marshalState(state), s.wire)
}

// marshalState passes strings through untouched and lets everything else be
// encoded as JSON, matching the string, object or array the API accepts.
func marshalState(state any) any {
	if s, ok := state.(string); ok {
		return s
	}
	return state
}
