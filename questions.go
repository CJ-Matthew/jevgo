package jev

import "fmt"

// Question is one typed question in a Set.
//
// The interface is sealed: Jev defines exactly three primitives, so Choice,
// Score and Noul are the only implementations and always will be.
type Question interface {
	name() string
	wire() (wireQuestion, error)
	validate() error
}

// wireQuestion is the on-the-wire shape of a question.
type wireQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Limits the API documents on question shape.
const (
	MaxChoiceOptions = 255
	MinScoreLevels   = 2
	MaxScoreLevels   = 10
)

// Choice asks the model to pick one of a fixed set of options.
//
// T is your own option type, so the compiler checks that every criteria key is
// a real option and the answer you switch on is the same type you declared:
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
// Criteria values are any because the API accepts a string, object or array as
// an option description. A plain string is the common case.
type Choice[T ~string] struct {
	// Name is the key this question is sent and returned under. It must be
	// unique within a Set.
	Name string
	// Instructions is what to decide. String, object or array.
	Instructions any
	// Criteria maps each option to its description. At least 2, at most 255.
	Criteria map[T]any
}

func (c Choice[T]) name() string { return c.Name }

func (c Choice[T]) validate() error {
	if c.Name == "" {
		return fmt.Errorf("choice question: Name is empty")
	}
	if c.Instructions == nil {
		return fmt.Errorf("choice question %q: Instructions is nil", c.Name)
	}
	if len(c.Criteria) < 2 {
		return fmt.Errorf("choice question %q: needs at least 2 options, has %d", c.Name, len(c.Criteria))
	}
	if len(c.Criteria) > MaxChoiceOptions {
		return fmt.Errorf("choice question %q: %d options exceeds the limit of %d", c.Name, len(c.Criteria), MaxChoiceOptions)
	}
	for opt, desc := range c.Criteria {
		if opt == "" {
			return fmt.Errorf("choice question %q: has an empty option name", c.Name)
		}
		if desc == nil {
			return fmt.Errorf("choice question %q: option %q has a nil description", c.Name, string(opt))
		}
	}
	return nil
}

func (c Choice[T]) wire() (wireQuestion, error) {
	crit := make(map[string]any, len(c.Criteria))
	for opt, desc := range c.Criteria {
		crit[string(opt)] = desc
	}
	return wireQuestion{Type: "choice", Instructions: c.Instructions, Criteria: crit}, nil
}

// Score asks the model to rate the state against ordered levels.
//
// Criteria is ordered lowest to highest; the answer is a float across that
// range, not an index, so ScoreAnswer exposes both the raw Score and a rounded
// Level.
type Score struct {
	// Name is the key this question is sent and returned under.
	Name string
	// Instructions is what to rate. String, object or array.
	Instructions any
	// Criteria are the ordered levels, lowest first. Between 2 and 10.
	Criteria []any
}

func (s Score) name() string { return s.Name }

func (s Score) validate() error {
	if s.Name == "" {
		return fmt.Errorf("score question: Name is empty")
	}
	if s.Instructions == nil {
		return fmt.Errorf("score question %q: Instructions is nil", s.Name)
	}
	if len(s.Criteria) < MinScoreLevels || len(s.Criteria) > MaxScoreLevels {
		return fmt.Errorf("score question %q: needs between %d and %d levels, has %d",
			s.Name, MinScoreLevels, MaxScoreLevels, len(s.Criteria))
	}
	for i, lvl := range s.Criteria {
		if lvl == nil {
			return fmt.Errorf("score question %q: level %d is nil", s.Name, i)
		}
	}
	return nil
}

func (s Score) wire() (wireQuestion, error) {
	return wireQuestion{Type: "score", Instructions: s.Instructions, Criteria: s.Criteria}, nil
}

// Noul asks whether something is true and returns a probability.
//
// Criteria is optional. When set, describe both outcomes.
//
// Note that Jev returns no confidence for a Noul, only the probability. See
// NoulAnswer for what that means for thresholds.
type Noul struct {
	// Name is the key this question is sent and returned under.
	Name string
	// Instructions is the proposition to evaluate. String, object or array.
	Instructions any
	// Criteria optionally describes what true and false look like.
	Criteria map[bool]any
}

func (n Noul) name() string { return n.Name }

func (n Noul) validate() error {
	if n.Name == "" {
		return fmt.Errorf("noul question: Name is empty")
	}
	if n.Instructions == nil {
		return fmt.Errorf("noul question %q: Instructions is nil", n.Name)
	}
	for k, v := range n.Criteria {
		if v == nil {
			return fmt.Errorf("noul question %q: criteria %t is nil", n.Name, k)
		}
	}
	return nil
}

func (n Noul) wire() (wireQuestion, error) {
	q := wireQuestion{Type: "noul", Instructions: n.Instructions}
	if len(n.Criteria) > 0 {
		crit := make(map[string]any, len(n.Criteria))
		for k, v := range n.Criteria {
			if k {
				crit["true"] = v
			} else {
				crit["false"] = v
			}
		}
		q.Criteria = crit
	}
	return q, nil
}
