# jevgo

A Go client for [Jev](https://typesafe.ai), TypeSafe's System One model.

Jev answers typed questions instead of generating text. You give it state and a
set of questions with declared shapes; it returns values your code can use
directly, each with a probability distribution and a confidence score. It never
emits a string, so it cannot return a value you did not offer it.

No dependencies outside the standard library.

```
go get github.com/CJ-Matthew/jevgo
```

## Quick start

```go
type Dept string

const (
	Billing   Dept = "billing"
	Technical Dept = "technical"
	Sales     Dept = "sales"
)

type Ticket struct {
	Body string `json:"body"`
	Plan string `json:"plan"`
}

var (
	Department = jev.Choice[Dept]{
		Name:         "department",
		Instructions: "Which team should handle this",
		Criteria: map[Dept]any{
			Billing:   "Invoices, refunds, charges, VAT",
			Technical: "Defects, outages, API errors",
			Sales:     "Pricing, quotes, plans, upgrades",
		},
	}

	Frustration = jev.Score{
		Name:         "frustration",
		Instructions: "How frustrated the customer appears",
		Criteria:     []any{"Calm", "Frustrated but civil", "Very angry"},
	}

	Urgent = jev.Noul{
		Name:         "is_urgent",
		Instructions: "The ticket is time-sensitive or something is blocked",
	}

	TicketSet = jev.MustNewSet[Ticket](Department, Frustration, Urgent)
)

client := jev.New() // reads TYPESAFE_API_KEY

res, err := TicketSet.Eval(ctx, client, Ticket{Body: body, Plan: "growth"})
if err != nil {
	return err
}

dept, err := Department.From(res)
switch dept.Choice { // a Dept, not a string
case Technical:
	assign(engineering)
case Billing:
	assign(finance)
}
```

## What the types buy you

**Options are checked at compile time.** `Choice[Dept]` keys its criteria by
your own type, so an option that isn't a `Dept` doesn't build, and the answer
you switch on is the same type you declared.

**Sets are bound to their state.** `Set[Ticket]` only evaluates tickets.
Pointing a ticket question set at a resume is a compile error.

**Mistakes surface at startup.** `MustNewSet` panics at package initialisation
on a duplicate question name, empty criteria, a Choice over more than 255
options or a Score outside 2–10 levels. `NewSet` returns an error for sets
assembled at runtime.

**The model can't answer off-script.** If the API ever returns an option that
wasn't offered, `From` reports an error rather than handing back a value typed
as an option that isn't one.

## What it deliberately doesn't do

This library reports what the model returned and stops. It has no opinion about
what confidence is high enough to act on, because that depends on what being
wrong costs you.

So there is no gating, no auto/confirm/human routing, no recorder, and no
default threshold anywhere except one: `Noul.True()` compares against 0.5,
which is the midpoint between two outcomes rather than a number anyone chose.
Every other predicate makes you pass the threshold:

```go
dept.Confidence          // as returned
dept.Probabilities       // map[Dept]float64, the full distribution
dept.Confident(0.9)      // threshold is always explicit

frustration.Score        // 1.4 — the raw float, not an index
frustration.Level        // 1  — rounded
frustration.Confident(0.8)

urgent.Prob              // the only field the API returns for a Noul
urgent.True()            // > 0.5
urgent.TrueAbove(0.8)
v, ok := urgent.Decisive(0.1) // committed either way?
```

### Why Noul is different

Jev returns **no confidence for a Noul**, only a probability, so this package
does not invent one. Confidence elsewhere is derived from how a distribution is
spread, and the TypeSafe docs are explicit that `P(noul)` and `P(not noul)` are
not guaranteed to sum to 1 — so any confidence computed from `Prob` alone rests
on an assumption TypeSafe does not make.

`Decisive(tol)` makes that assumption out loud, and returns two values because
"not true" and "wouldn't commit" are different answers:

```go
v, ok := urgent.Decisive(0.1)
// p = 0.93 -> (true,  true)   decisively yes
// p = 0.04 -> (false, true)   decisively no
// p = 0.52 -> (false, false)  technically true, but the model is shrugging
```

Tune `tol` against your own data. Don't carry a Choice threshold over to it.

## Asking many questions at once

Questions in a set are answered independently and in parallel, so adding one
costs very little. Ask everything up front — including questions only some
branches need — and discard what you don't use, rather than making a second
call to find out.

## Batching

```go
results, err := jev.Map(ctx, client, TicketSet, tickets,
	jev.WithConcurrency(16),
	jev.WithRateLimit(jev.DefaultLimits), // 1,200 rpm / 250k tokens per second
)

for _, r := range results {
	if r.Err != nil {
		log.Printf("ticket %d: %v", r.Index, r.Err)
		continue
	}
	dept, _ := Department.From(r.Result)
	...
}
```

Results come back in input order, each carrying its own error, so one failure
doesn't stop the rest. Token cost isn't known until a response arrives, so an
estimate is reserved up front and reconciled with reported usage afterwards:
the goal is to hold sustained throughput under the ceiling, not to be exact
about any single request.

## Errors

```go
var apiErr *jev.APIError
if errors.As(err, &apiErr) {
	log.Println(apiErr.StatusCode, apiErr.Message, apiErr.RetryAfter)
}

if errors.Is(err, jev.ErrRateLimit) { ... }
```

Sentinels: `ErrNoAPIKey`, `ErrAuth` (401), `ErrValidation` (422),
`ErrRateLimit` (429), `ErrOverloaded` (529).

429, 529 and 5xx are retried with exponential backoff and full jitter, honouring
`Retry-After` when present. 401 and 422 are returned immediately — another
attempt cannot change the outcome.

## Testing

The test seam swaps the HTTP transport rather than the client, so retries,
backoff, status handling and decoding all still run against fixture bytes.

```go
client := jev.New(
	jev.WithAPIKey("test"),
	jev.WithTransport(jevtest.Static(fixture)),
)

// a retry, end to end
jevtest.Sequence(
	jevtest.Status(429, ""),
	jevtest.Static(fixture),
)

// a directory of hand-written fixtures
rep, err := jevtest.Replay(os.DirFS("testdata"))
client := jev.New(jev.WithAPIKey("test"), jev.WithTransport(rep))
rep.Requests() // what was actually sent
```

Fixtures are JSON with an optional `match` substring and `status`:

```json
{
  "match": "Stripe",
  "response": {
    "model": "jev-1.13.0",
    "answers": {
      "department": {"type": "choice", "choice": "technical",
                     "probabilities": {"technical": 0.94},
                     "confidence": 0.91}
    },
    "usage": {"input_tokens": 120, "output_tokens": 0}
  }
}
```

The first fixture whose `match` appears in the request body wins; one without a
`match` catches everything. Note the request carries your question criteria as
well as your state, so match on a phrase distinctive to the state.

## Configuration

```go
jev.New(
	jev.WithAPIKey(key),          // else TYPESAFE_API_KEY
	jev.WithModel("jev-1.13.0"),  // else jev-latest
	jev.WithBaseURL(url),
	jev.WithHTTPClient(h),
	jev.WithTransport(rt),
	jev.WithRetry(jev.Retry{MaxAttempts: 3, Base: 200 * time.Millisecond}),
)
```

## API facts this wraps

| | |
|---|---|
| Endpoint | `POST /v1/systemone` |
| Model | `jev-1.13.0`, alias `jev-latest` |
| Context | 64K total; 32K for state plus the longest question |
| Rate limits | 1,200 req/min, 250,000 tokens/sec |
| Price | $0.042 per million input tokens; output free |
| Choice | 2–255 options |
| Score | 2–10 levels |
| Input | Text only — string, JSON object or array. No images, audio or video |

`Instructions` and criteria descriptions are typed `any` because the API accepts
a string, object or array for each. A plain string is the common case.

## Limitations worth knowing

These are the model's, not this library's, and they inform how you write
questions:

- It does not do arithmetic or count reliably. Compute in code.
- It reads dates as text, not ordered quantities. Compare in code.
- It reads instructions literally — it answers what you wrote.
- Large irrelevant state distracts it. Filter before sending.
- It does not treat state as hostile. Injected text can move an answer.
- It generates no text at all. Extract by offering candidates as a Choice.

See [model jaggedness](https://docs.typesafe.ai/model-jaggedness/jev-1.13).
