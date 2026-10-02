package questions

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/batuta-ai/core/judge"
)

func TestBuildRequest(t *testing.T) {
	t.Parallel()
	req := BuildRequest("May I change this?", "The task permits a change.")
	if req.Decision != "question_match" {
		t.Errorf("Decision = %q", req.Decision)
	}
	state, err := json.Marshal(req.State)
	if err != nil {
		t.Fatal(err)
	}
	if string(state) != `{"question":"May I change this?","passage":"The task permits a change."}` {
		t.Errorf("State = %s", state)
	}
	if len(req.Questions) != 1 {
		t.Fatalf("Questions = %#v", req.Questions)
	}
	question, ok := req.Questions["answer"]
	if !ok || question.Type != judge.QuestionChoice {
		t.Fatalf("answer question = %#v", question)
	}
	criteria, ok := question.Criteria.(map[string]string)
	if !ok {
		t.Fatalf("Criteria = %#v", question.Criteria)
	}
	want := map[string]string{
		"answered_here": "a sentence of the passage states what the question asks, or states the rule that decides it",
		"not_addressed": "no sentence of the passage addresses what the question asks",
		"insufficient":  "the question or the passage is too unclear to choose",
	}
	if !reflect.DeepEqual(criteria, want) {
		t.Errorf("Criteria = %#v, want %#v", criteria, want)
	}
	if question.Instructions != "Does a sentence of the task's passage state what the executor's question asks, or state the rule that decides it?" {
		t.Errorf("Instructions = %#v", question.Instructions)
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		answer judge.Answer
		want   Decision
	}{
		{"answered here at threshold", judge.Answer{Type: judge.QuestionChoice, Choice: "answered_here", Confidence: 0.9}, Decision{Status: "firm", Option: "answered_here"}},
		{"not addressed above threshold", judge.Answer{Type: judge.QuestionChoice, Choice: "not_addressed", Confidence: 0.99}, Decision{Status: "firm", Option: "not_addressed"}},
		{"insufficient", judge.Answer{Type: judge.QuestionChoice, Choice: "insufficient", Confidence: 0.99}, Decision{Status: "insufficient"}},
		{"below threshold", judge.Answer{Type: judge.QuestionChoice, Choice: "answered_here", Confidence: 0.899}, Decision{Status: "below_threshold"}},
		{"unknown choice", judge.Answer{Type: judge.QuestionChoice, Choice: "yes", Confidence: 0.99}, Decision{Status: "unavailable"}},
		{"missing answer", judge.Answer{}, Decision{Status: "unavailable"}},
		{"wrong type", judge.Answer{Type: judge.QuestionNoul, Choice: "answered_here", Confidence: 0.99}, Decision{Status: "unavailable"}},
		{"invalid confidence", judge.Answer{Type: judge.QuestionChoice, Choice: "answered_here", Confidence: math.NaN()}, Decision{Status: "unavailable"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Decide(tt.answer, 0.9); got != tt.want {
				t.Errorf("Decide(%#v, 0.9) = %#v, want %#v", tt.answer, got, tt.want)
			}
		})
	}
}
