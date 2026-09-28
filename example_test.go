package jev_test

import (
	"context"
	"fmt"

	jev "github.com/CJ-Matthew/jevgo"
	"github.com/CJ-Matthew/jevgo/jevtest"
)

// Department is a Choice over your own option type, so the compiler checks
// both the criteria you declare and the answer you switch on.
type Department string

const (
	Billing   Department = "billing"
	Technical Department = "technical"
	Sales     Department = "sales"
)

// SupportTicket is the state the question set evaluates.
type SupportTicket struct {
	Body string `json:"body"`
	Plan string `json:"plan"`
}

var (
	deptQuestion = jev.Choice[Department]{
		Name:         "department",
		Instructions: "Which team should handle this",
		Criteria: map[Department]any{
			Billing:   "Invoices, refunds, charges, VAT",
			Technical: "Software defects, service interruptions, API errors",
			Sales:     "Pricing, quotes, plans, upgrades",
		},
	}

	frustrationQuestion = jev.Score{
		Name:         "frustration",
		Instructions: "How frustrated the customer appears",
		Criteria: []any{
			"Calm, just stating facts",
			"Frustrated but civil",
			"Very angry, strong language",
		},
	}

	urgentQuestion = jev.Noul{
		Name:         "is_urgent",
		Instructions: "The ticket is time-sensitive or something is blocked",
	}

	// A malformed set panics here, at package initialisation, rather than on
	// the first request in production.
	ticketSet = jev.MustNewSet[SupportTicket](
		deptQuestion, frustrationQuestion, urgentQuestion,
	)
)

// canned stands in for the API so the example runs offline.
const canned = `{
  "model": "jev-1.13.0",
  "answers": {
    "department":  {"type":"choice","choice":"technical",
                    "probabilities":{"technical":0.94,"billing":0.04,"sales":0.02},
                    "confidence":0.91},
    "frustration": {"type":"score","score":1.4,
                    "probabilities":{"0":0.10,"1":0.50,"2":0.40},
                    "legend":{"0":"Calm","1":"Frustrated but civil","2":"Very angry"},
                    "confidence":0.62},
    "is_urgent":   {"type":"noul","noul":0.93}
  },
  "usage": {"input_tokens":120,"output_tokens":0}
}`

func Example() {
	client := jev.New(
		jev.WithAPIKey("example"),
		jev.WithTransport(jevtest.Static(canned)),
	)

	ticket := SupportTicket{
		Body: "Our Stripe integration has been failing for three days and we are losing sales.",
		Plan: "growth",
	}

	res, err := ticketSet.Eval(context.Background(), client, ticket)
	if err != nil {
		fmt.Println("eval failed:", err)
		return
	}

	dept, err := deptQuestion.From(res)
	if err != nil {
		fmt.Println(err)
		return
	}
	frustration, err := frustrationQuestion.From(res)
	if err != nil {
		fmt.Println(err)
		return
	}
	urgent, err := urgentQuestion.From(res)
	if err != nil {
		fmt.Println(err)
		return
	}

	// dept.Choice is a Department, so this switch is over your own type.
	switch dept.Choice {
	case Technical:
		fmt.Println("route: engineering")
	case Billing:
		fmt.Println("route: finance")
	case Sales:
		fmt.Println("route: sales")
	}

	// Thresholds are yours. The library reports, it does not decide.
	fmt.Printf("confident enough to auto-assign: %t\n", dept.Confident(0.9))
	fmt.Printf("frustration: %.1f (level %d, %q)\n",
		frustration.Score, frustration.Level, frustration.Legend[frustration.Level])

	// Noul has no confidence field, so decisiveness is asked for explicitly.
	if v, ok := urgent.Decisive(0.1); ok {
		fmt.Printf("urgent: %t\n", v)
	} else {
		fmt.Printf("urgent: undecided at p=%.2f\n", urgent.Prob)
	}

	// Output:
	// route: engineering
	// confident enough to auto-assign: true
	// frustration: 1.4 (level 1, "Frustrated but civil")
	// urgent: true
}
