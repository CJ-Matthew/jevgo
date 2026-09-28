package jev

import (
	"strings"
	"testing"
)

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
	department = Choice[Dept]{
		Name:         "department",
		Instructions: "Which team should handle this",
		Criteria: map[Dept]any{
			Billing:   "Invoices, refunds, charges",
			Technical: "Bugs, outages, API errors",
			Sales:     "Pricing, quotes, upgrades",
		},
	}
	frustration = Score{
		Name:         "frustration",
		Instructions: "How frustrated the customer appears",
		Criteria:     []any{"Calm", "Frustrated but civil", "Very angry"},
	}
	urgent = Noul{
		Name:         "is_urgent",
		Instructions: "The ticket is time-sensitive",
		Criteria:     map[bool]any{true: "Blocked or deadline", false: "No time pressure"},
	}
)

func TestNewSetRejectsBadQuestions(t *testing.T) {
	tests := []struct {
		name string
		q    Question
		want string
	}{
		{"no name", Choice[Dept]{Instructions: "x", Criteria: map[Dept]any{Billing: "a", Sales: "b"}}, "Name is empty"},
		{"nil instructions", Choice[Dept]{Name: "d", Criteria: map[Dept]any{Billing: "a", Sales: "b"}}, "Instructions is nil"},
		{"one option", Choice[Dept]{Name: "d", Instructions: "x", Criteria: map[Dept]any{Billing: "a"}}, "at least 2 options"},
		{"too few levels", Score{Name: "s", Instructions: "x", Criteria: []any{"only"}}, "between 2 and 10 levels"},
		{"too many levels", Score{Name: "s", Instructions: "x", Criteria: make([]any, 11)}, "between 2 and 10 levels"},
		{"noul no name", Noul{Instructions: "x"}, "Name is empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSet[Ticket](tc.q)
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestNewSetRejectsTooManyOptions(t *testing.T) {
	crit := make(map[Dept]any, MaxChoiceOptions+1)
	for i := range MaxChoiceOptions + 1 {
		crit[Dept(string(rune('a'+i%26))+string(rune('a'+i/26)))] = "x"
	}
	if len(crit) <= MaxChoiceOptions {
		t.Fatalf("test setup made only %d options", len(crit))
	}
	_, err := NewSet[Ticket](Choice[Dept]{Name: "d", Instructions: "x", Criteria: crit})
	if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("expected a limit error, got %v", err)
	}
}

func TestNewSetRejectsDuplicateNames(t *testing.T) {
	dup := Noul{Name: "department", Instructions: "also called department"}
	_, err := NewSet[Ticket](department, dup)
	if err == nil || !strings.Contains(err.Error(), "duplicate question name") {
		t.Fatalf("expected a duplicate-name error, got %v", err)
	}
}

func TestMustNewSetPanicsOnBadSet(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	MustNewSet[Ticket](Score{Name: "s", Instructions: "x", Criteria: []any{"one"}})
}

func TestSetNamesInDeclarationOrder(t *testing.T) {
	set := MustNewSet[Ticket](department, frustration, urgent)
	got := set.Names()
	want := []string{"department", "frustration", "is_urgent"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestNoulWireUsesStringKeys(t *testing.T) {
	w, err := urgent.wire()
	if err != nil {
		t.Fatal(err)
	}
	crit, ok := w.Criteria.(map[string]any)
	if !ok {
		t.Fatalf("criteria is %T, want map[string]any", w.Criteria)
	}
	if crit["true"] != "Blocked or deadline" || crit["false"] != "No time pressure" {
		t.Fatalf("unexpected criteria: %v", crit)
	}
}
