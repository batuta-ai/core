package classify

import (
	"math"
	"path"
	"strings"

	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

const v2DecisionName = "classify_v2"

var v2QuestionOrder = []string{"contract", "lifecycle", "security", "open_decision", "mechanical"}

type ScopeFeatures struct {
	Files       int
	Directories int
	TestOnly    bool
	DocsOnly    bool
}

type DecisionV2 struct {
	Complexity routing.Complexity
	Answers    map[string]bool
	Defaulted  []string
}

// Features counts Scope entries as written, including globs.
func Features(task routing.PlanTask) ScopeFeatures {
	features := ScopeFeatures{Files: len(task.Scope)}
	if len(task.Scope) == 0 {
		return features
	}
	features.TestOnly = true
	features.DocsOnly = true
	directories := make(map[string]struct{}, len(task.Scope))
	for _, entry := range task.Scope {
		directories[path.Dir(entry)] = struct{}{}
		name := path.Base(entry)
		if !strings.Contains(name, "_test.") && !underDirectory(entry, "testdata") {
			features.TestOnly = false
		}
		if !strings.HasSuffix(name, ".md") && !underDirectory(entry, "docs") {
			features.DocsOnly = false
		}
	}
	features.Directories = len(directories)
	return features
}

func underDirectory(entry, directory string) bool {
	return strings.Contains("/"+entry, "/"+directory+"/")
}

// BuildRequestV2 asks only the five frozen yes/no questions over v1's bounded task state.
func BuildRequestV2(task routing.PlanTask, context string) judge.Request {
	request := BuildRequest(task, context)
	request.Decision = v2DecisionName
	request.Questions = map[string]judge.Question{
		"contract": {
			Type:         judge.QuestionNoul,
			Instructions: "does it change a public or cross-package contract (exported API, CLI flag, file format, protocol)?",
		},
		"lifecycle": {
			Type:         judge.QuestionNoul,
			Instructions: "does it involve concurrency, process lifecycle, I/O timing or retries?",
		},
		"security": {
			Type:         judge.QuestionNoul,
			Instructions: "is it security-sensitive (permissions, sandbox, secrets, redaction)?",
		},
		"open_decision": {
			Type:         judge.QuestionNoul,
			Instructions: "does it depend on a decision the plan does not state?",
		},
		"mechanical": {
			Type:         judge.QuestionNoul,
			Instructions: "is it purely mechanical (rename, copy, config, documentation wording)?",
		},
	}
	return request
}

// DecideV2 applies the frozen first-match lane rule to measured Scope and judge answers.
func DecideV2(features ScopeFeatures, answers map[string]judge.Answer) DecisionV2 {
	decision := DecisionV2{Answers: make(map[string]bool, len(v2QuestionOrder))}
	for _, key := range v2QuestionOrder {
		value, ok := noulAnswer(answers[key])
		if !ok {
			value = key != "mechanical"
			decision.Defaulted = append(decision.Defaulted, key)
		}
		decision.Answers[key] = value
	}

	contract := decision.Answers["contract"]
	lifecycle := decision.Answers["lifecycle"]
	security := decision.Answers["security"]
	switch {
	case decision.Answers["open_decision"]:
		decision.Complexity = routing.ComplexityCritical
	case decision.Answers["mechanical"] && features.Files <= 2 && !contract && !lifecycle && !security:
		decision.Complexity = routing.ComplexityLow
	case security || (contract && lifecycle) || (contract && security) || (lifecycle && security) || features.Directories >= 3:
		decision.Complexity = routing.ComplexityHigh
	default:
		decision.Complexity = routing.ComplexityMedium
	}
	return decision
}

func noulAnswer(answer judge.Answer) (bool, bool) {
	if answer.Type != judge.QuestionNoul || math.IsNaN(answer.Noul) || answer.Noul < 0 || answer.Noul > 1 {
		return false, false
	}
	if math.Max(answer.Noul, 1-answer.Noul) < 0.7 {
		return false, false
	}
	return answer.Noul >= 0.5, true
}
