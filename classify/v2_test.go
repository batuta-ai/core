package classify

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

func TestFeatures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		scope []string
		want  ScopeFeatures
	}{
		{"empty", nil, ScopeFeatures{}},
		{"root files", []string{"README.md", "LICENSE.md"}, ScopeFeatures{Files: 2, Directories: 1, DocsOnly: true}},
		{"test files and testdata", []string{"classify/v2_test.go", "classify/testdata/input.json", "other/*_test.go"}, ScopeFeatures{Files: 3, Directories: 3, TestOnly: true}},
		{"documentation directories", []string{"docs/guide.txt", "other/docs/page.json", "README.md"}, ScopeFeatures{Files: 3, Directories: 3, DocsOnly: true}},
		{"mixed entries", []string{"classify/v2.go", "classify/v2_test.go", "docs/guide.md", "classify/*.go"}, ScopeFeatures{Files: 4, Directories: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Features(routing.PlanTask{Scope: tc.scope})
			if got != tc.want {
				t.Fatalf("Features() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestBuildRequestV2(t *testing.T) {
	t.Parallel()

	task := routing.PlanTask{
		TaskArtifact: routing.TaskArtifact{
			ID:         "task_1",
			Title:      "Change the public format",
			Status:     "pending",
			Domain:     routing.DomainBackend,
			Complexity: routing.ComplexityHigh,
		},
		Number:   1,
		Executor: "host-executor",
		Model:    "host-model",
		Scope:    []string{"classify/v2.go"},
		Accept:   []string{"format is updated"},
	}
	context := strings.Join([]string{
		"Shared context.",
		"**Task 2.** Other task only.",
		"**Task 1.** Relevant decision.",
		"API_TOKEN=secret-value",
		strings.Repeat("z", 4100),
	}, "\n\n")
	req := BuildRequestV2(task, context)
	if req.Decision != "classify_v2" {
		t.Fatalf("Decision = %q", req.Decision)
	}
	if !reflect.DeepEqual(req.State, BuildRequest(task, context).State) {
		t.Fatalf("v2 state differs from bounded v1 task/context: %#v", req.State)
	}
	encoded, err := json.Marshal(req.State)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"backend", "high", "host-executor", "host-model", "secret-value", "Other task only."} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("state carries %q: %s", forbidden, encoded)
		}
	}
	if len(req.Questions) != 5 {
		t.Fatalf("questions = %#v", req.Questions)
	}
	want := map[string]string{
		"contract":      "does it change a public or cross-package contract (exported API, CLI flag, file format, protocol)?",
		"lifecycle":     "does it involve concurrency, process lifecycle, I/O timing or retries?",
		"security":      "is it security-sensitive (permissions, sandbox, secrets, redaction)?",
		"open_decision": "does it depend on a decision the plan does not state?",
		"mechanical":    "is it purely mechanical (rename, copy, config, documentation wording)?",
	}
	for key, wording := range want {
		question, ok := req.Questions[key]
		if !ok || question.Type != judge.QuestionNoul || question.Instructions != wording {
			t.Fatalf("question %q = %#v, want noul %q", key, question, wording)
		}
	}
}

func TestDecideV2Rule(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		features ScopeFeatures
		yes      []string
		want     routing.Complexity
	}{
		{"open decision first", ScopeFeatures{Files: 1, Directories: 1}, []string{"open_decision", "mechanical", "security"}, routing.ComplexityCritical},
		{"mechanical low", ScopeFeatures{Files: 2, Directories: 1}, []string{"mechanical"}, routing.ComplexityLow},
		{"mechanical too many files", ScopeFeatures{Files: 3, Directories: 1}, []string{"mechanical"}, routing.ComplexityMedium},
		{"contract blocks low", ScopeFeatures{Files: 1, Directories: 1}, []string{"mechanical", "contract"}, routing.ComplexityMedium},
		{"lifecycle blocks low", ScopeFeatures{Files: 1, Directories: 1}, []string{"mechanical", "lifecycle"}, routing.ComplexityMedium},
		{"security high", ScopeFeatures{Files: 1, Directories: 1}, []string{"mechanical", "security"}, routing.ComplexityHigh},
		{"two signals high", ScopeFeatures{Files: 1, Directories: 1}, []string{"contract", "lifecycle"}, routing.ComplexityHigh},
		{"three directories high", ScopeFeatures{Files: 1, Directories: 3}, nil, routing.ComplexityHigh},
		{"low before directories", ScopeFeatures{Files: 2, Directories: 3}, []string{"mechanical"}, routing.ComplexityLow},
		{"otherwise medium", ScopeFeatures{Files: 2, Directories: 2}, []string{"contract"}, routing.ComplexityMedium},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answers := v2Answers(tc.yes...)
			got := DecideV2(tc.features, answers)
			if got.Complexity != tc.want || len(got.Defaulted) != 0 {
				t.Fatalf("DecideV2() = %#v, want %s without defaults", got, tc.want)
			}
		})
	}
}

func TestDecideV2Uncertainty(t *testing.T) {
	t.Parallel()

	features := ScopeFeatures{Files: 1, Directories: 1}
	got := DecideV2(features, nil)
	if got.Complexity != routing.ComplexityCritical || !reflect.DeepEqual(got.Defaulted, []string{"contract", "lifecycle", "security", "open_decision", "mechanical"}) {
		t.Fatalf("missing answers = %#v", got)
	}
	for _, key := range []string{"contract", "lifecycle", "security", "open_decision"} {
		if !got.Answers[key] {
			t.Fatalf("missing %s defaulted to no: %#v", key, got)
		}
	}
	if got.Answers["mechanical"] {
		t.Fatalf("missing mechanical defaulted to yes: %#v", got)
	}

	answers := v2Answers("mechanical")
	answers["contract"] = judge.Answer{Type: judge.QuestionNoul, Noul: 0.31}
	answers["mechanical"] = judge.Answer{Type: judge.QuestionNoul, Noul: 0.69}
	got = DecideV2(features, answers)
	if got.Complexity != routing.ComplexityMedium || !reflect.DeepEqual(got.Defaulted, []string{"contract", "mechanical"}) || !got.Answers["contract"] || got.Answers["mechanical"] {
		t.Fatalf("uncertain answers = %#v", got)
	}

	answers["contract"] = judge.Answer{Type: judge.QuestionNoul, Noul: 0.3}
	answers["mechanical"] = judge.Answer{Type: judge.QuestionNoul, Noul: 0.7}
	got = DecideV2(features, answers)
	if got.Complexity != routing.ComplexityLow || len(got.Defaulted) != 0 || got.Answers["contract"] || !got.Answers["mechanical"] {
		t.Fatalf("confidence boundary = %#v", got)
	}

	answers["security"] = judge.Answer{Type: judge.QuestionChoice, Choice: "no", Confidence: 1}
	got = DecideV2(features, answers)
	if got.Complexity != routing.ComplexityHigh || !reflect.DeepEqual(got.Defaulted, []string{"security"}) {
		t.Fatalf("wrong answer type = %#v", got)
	}
}

func v2Answers(yes ...string) map[string]judge.Answer {
	answers := make(map[string]judge.Answer)
	for _, key := range []string{"contract", "lifecycle", "security", "open_decision", "mechanical"} {
		answers[key] = judge.Answer{Type: judge.QuestionNoul, Noul: 0.05}
	}
	for _, key := range yes {
		answers[key] = judge.Answer{Type: judge.QuestionNoul, Noul: 0.95}
	}
	return answers
}
