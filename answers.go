package jev

import (
	"math"
	"strconv"
	"time"
)

// Usage is the token accounting the API returns. Jev does not charge for
// output tokens.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// rawAnswer is one decoded answer, before it is narrowed to a typed one.
type rawAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul"`
	Choice        *string            `json:"choice"`
	Score         *float64           `json:"score"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Legend        map[string]any     `json:"legend"`
}

// Result is one evaluated request.
//
// Answers are read through the question that asked them, which is what makes
// them typed:
//
//	res, err := TicketSet.Eval(ctx, client, ticket)
//	dept, err := Department.From(res)
type Result struct {
	// Model is the exact model version that answered, e.g. "jev-1.13.0".
	Model string
	// Usage is the token accounting for this request.
	Usage Usage
	// Latency is measured client-side around the HTTP call, so it includes
	// network time. It is not reported by the API.
	Latency time.Duration

	answers map[string]rawAnswer
}

// Names lists the question keys present in the response.
func (r *Result) Names() []string {
	out := make([]string, 0, len(r.answers))
	for k := range r.answers {
		out = append(out, k)
	}
	return out
}

// ChoiceAnswer is the answer to a Choice question, typed to your option type.
type ChoiceAnswer[T ~string] struct {
	// Choice is the selected option. It is guaranteed to be one of the
	// options you declared.
	Choice T
	// Confidence is how concentrated the probability distribution is, as
	// reported by the model: 1.0 when it sits entirely on one option, lower
	// the more it spreads.
	Confidence float64
	// Probabilities is the full distribution over your options.
	Probabilities map[T]float64
}

// Confident reports whether Confidence is at least threshold.
//
// There is no default threshold, and deliberately so: what counts as confident
// enough depends on what acting on the answer costs, which is yours to decide.
func (a ChoiceAnswer[T]) Confident(threshold float64) bool {
	return a.Confidence >= threshold
}

// From narrows a Result to this question's typed answer.
func (c Choice[T]) From(r *Result) (ChoiceAnswer[T], error) {
	var zero ChoiceAnswer[T]
	raw, err := lookup(r, c.Name, "choice")
	if err != nil {
		return zero, err
	}
	if raw.Choice == nil {
		return zero, &AnswerError{Question: c.Name, Reason: "response has no choice field"}
	}

	picked := T(*raw.Choice)
	if _, ok := c.Criteria[picked]; !ok {
		// Cannot happen while the API honours the schema, but if it ever
		// does not, failing here beats handing back a value that is typed
		// as an option and is not one.
		return zero, &AnswerError{
			Question: c.Name,
			Reason:   "model returned option " + strconv.Quote(*raw.Choice) + " which was not offered",
		}
	}

	probs := make(map[T]float64, len(raw.Probabilities))
	for k, v := range raw.Probabilities {
		probs[T(k)] = v
	}
	return ChoiceAnswer[T]{
		Choice:        picked,
		Confidence:    deref(raw.Confidence),
		Probabilities: probs,
	}, nil
}

// ScoreAnswer is the answer to a Score question.
type ScoreAnswer struct {
	// Score is the raw value across the levels you supplied. It is a float,
	// so a ticket between "frustrated" and "very angry" can land at 1.4.
	Score float64
	// Level is Score rounded to the nearest level index.
	Level int
	// Confidence is how concentrated the distribution over levels is.
	Confidence float64
	// Legend maps level index to the criteria you supplied, as echoed back.
	Legend map[int]any
	// Probabilities is the full distribution over level indices.
	Probabilities map[int]float64
}

// Confident reports whether Confidence is at least threshold. As with Choice,
// there is no default.
func (a ScoreAnswer) Confident(threshold float64) bool {
	return a.Confidence >= threshold
}

// From narrows a Result to this question's answer.
func (s Score) From(r *Result) (ScoreAnswer, error) {
	var zero ScoreAnswer
	raw, err := lookup(r, s.Name, "score")
	if err != nil {
		return zero, err
	}
	if raw.Score == nil {
		return zero, &AnswerError{Question: s.Name, Reason: "response has no score field"}
	}

	probs := make(map[int]float64, len(raw.Probabilities))
	for k, v := range raw.Probabilities {
		if i, err := strconv.Atoi(k); err == nil {
			probs[i] = v
		}
	}
	legend := make(map[int]any, len(raw.Legend))
	for k, v := range raw.Legend {
		if i, err := strconv.Atoi(k); err == nil {
			legend[i] = v
		}
	}
	return ScoreAnswer{
		Score:         *raw.Score,
		Level:         int(math.Round(*raw.Score)),
		Confidence:    deref(raw.Confidence),
		Legend:        legend,
		Probabilities: probs,
	}, nil
}

// NoulAnswer is the answer to a Noul question.
//
// Jev returns a probability and nothing else for a Noul: there is no
// confidence field, so this type does not invent one. Confidence for Choice
// and Score is derived from how a distribution is spread, and the docs are
// explicit that P(noul) and P(not noul) are not guaranteed to sum to 1, so any
// "confidence" computed from Prob alone would rest on an assumption TypeSafe
// does not make. Use Decisive when you want that assumption, stated out loud.
type NoulAnswer struct {
	// Prob is the probability the proposition is true, exactly as returned.
	Prob float64
}

// True reports whether Prob is above 0.5.
//
// 0.5 is the midpoint between the two outcomes rather than a threshold anyone
// chose, which is why this is the one predicate in the package that takes no
// argument. For any real threshold, use TrueAbove.
func (a NoulAnswer) True() bool { return a.Prob > 0.5 }

// TrueAbove reports whether Prob is above threshold.
func (a NoulAnswer) TrueAbove(threshold float64) bool { return a.Prob > threshold }

// Decisive reports the outcome and whether the model committed to it.
//
// The answer is decisive when Prob is within tol of either end: Prob >= 1-tol
// is a decisive true, Prob <= tol a decisive false. Anything between returns
// ok false, which is the model declining to commit rather than a no. With
// tol 0.1 a Prob of 0.93 gives (true, true) and 0.52 gives (false, false).
//
// Note this treats Prob and 1-Prob as two ends of one scale. The API does not
// guarantee that. Tune tol against your own data rather than carrying a Choice
// or Score confidence threshold over to it.
func (a NoulAnswer) Decisive(tol float64) (value bool, ok bool) {
	switch {
	case a.Prob >= 1-tol:
		return true, true
	case a.Prob <= tol:
		return false, true
	default:
		return false, false
	}
}

// From narrows a Result to this question's answer.
func (n Noul) From(r *Result) (NoulAnswer, error) {
	var zero NoulAnswer
	raw, err := lookup(r, n.Name, "noul")
	if err != nil {
		return zero, err
	}
	if raw.Noul == nil {
		return zero, &AnswerError{Question: n.Name, Reason: "response has no noul field"}
	}
	return NoulAnswer{Prob: *raw.Noul}, nil
}

func lookup(r *Result, name, want string) (rawAnswer, error) {
	if r == nil {
		return rawAnswer{}, &AnswerError{Question: name, Reason: "nil result"}
	}
	raw, ok := r.answers[name]
	if !ok {
		return rawAnswer{}, &AnswerError{Question: name, Reason: "not present in response"}
	}
	if raw.Type != "" && raw.Type != want {
		return rawAnswer{}, &AnswerError{
			Question: name,
			Reason:   "expected a " + want + " answer, got " + raw.Type,
		}
	}
	return raw, nil
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}
