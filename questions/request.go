package questions

import (
	"math"

	"github.com/batuta-ai/core/judge"
)

const questionMatchInstructions = "Does a sentence of the task's passage state what the executor's question asks, or state the rule that decides it?"

var answerCriteria = map[string]string{
	"answered_here": "a sentence of the passage states what the question asks, or states the rule that decides it",
	"not_addressed": "no sentence of the passage addresses what the question asks",
	"insufficient":  "the question or the passage is too unclear to choose",
}

type requestState struct {
	Question string `json:"question"`
	Passage  string `json:"passage"`
}

// BuildRequest asks whether the task's passage states the answer.
func BuildRequest(question, passage string) judge.Request {
	return judge.Request{
		Decision: "question_match",
		State:    requestState{Question: question, Passage: passage},
		Questions: map[string]judge.Question{
			"answer": {
				Type:         judge.QuestionChoice,
				Instructions: questionMatchInstructions,
				Criteria:     answerCriteria,
			},
		},
	}
}

// Decision is an annotation of the judge answer; it never answers a question.
type Decision struct {
	Status string
	Option string
}

func Decide(answer judge.Answer, threshold float64) Decision {
	if answer.Type != judge.QuestionChoice || math.IsNaN(answer.Confidence) || math.IsInf(answer.Confidence, 0) {
		return Decision{Status: "unavailable"}
	}
	switch answer.Choice {
	case "insufficient":
		return Decision{Status: "insufficient"}
	case "answered_here", "not_addressed":
		if answer.Confidence < threshold {
			return Decision{Status: "below_threshold"}
		}
		return Decision{Status: "firm", Option: answer.Choice}
	default:
		return Decision{Status: "unavailable"}
	}
}
