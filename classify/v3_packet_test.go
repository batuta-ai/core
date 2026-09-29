package classify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

func TestContractPacket(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "v3")
	for _, tc := range []struct {
		name  string
		scope []string
		want  []string
	}{
		{"exact file", []string{"contracts.go"}, []string{"Exported", "Exported.PublicMethod", "PublicConstant", "PublicFunction", "PublicVariable", "RegisterFlags", "flag:alias", "flag:composite", "flag:count", "flag:delay", "flag:direct", "flag:global-name", "flag:ignored-not-first", "flag:local-name"}},
		{"glob excludes test and malformed", []string{"*.go"}, []string{"Exported", "Exported.PublicMethod", "PublicConstant", "PublicFunction", "PublicVariable", "RegisterFlags", "flag:alias", "flag:composite", "flag:count", "flag:delay", "flag:direct", "flag:global-name", "flag:ignored-not-first", "flag:local-name"}},
		{"empty", []string{"missing*.go"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, found := ContractPacket(root, tc.scope)
			if (len(tc.want) > 0 && !reflect.DeepEqual(got, tc.want)) || (len(tc.want) == 0 && len(got) != 0) || found != (len(tc.want) > 0) {
				t.Fatalf("ContractPacket() = %q, %v; want %q, %v", got, found, tc.want, len(tc.want) > 0)
			}
		})
	}
}

func TestContractPacketCap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var source strings.Builder
	source.WriteString("package fixture\n")
	for index := range 65 {
		fmt.Fprintf(&source, "var Exported%02d = %d\n", index, index)
	}
	if err := os.WriteFile(filepath.Join(root, "many.go"), []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	packet, found := ContractPacket(root, []string{"many.go"})
	if !found || len(packet) != 60 || packet[0] != "Exported00" || packet[59] != "Exported59" {
		t.Fatalf("ContractPacket() = %q, %v", packet, found)
	}
}

func TestContractPacketContained(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "valid.go"), []byte("package p\nfunc Inside() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken.go"), []byte("package p\nfunc Broken( {"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.go"), []byte("package p\nfunc Outside() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	got, found := ContractPacket(root, []string{"*.go", "../" + filepath.Base(outside) + "/outside.go", filepath.Join(outside, "outside.go")})
	if !found || !reflect.DeepEqual(got, []string{"Inside"}) {
		t.Fatalf("ContractPacket() = %q, %v; want [Inside], true", got, found)
	}
}

func TestSecurityPacket(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		found      bool
	}{
		{"permission", "internal/Permission_check.go", true},
		{"sandbox", "internal/sandbox.go", true},
		{"secret", "internal/secret.go", true},
		{"redact", "internal/redact.go", true},
		{"auth", "internal/auth.go", true},
		{"grant", "internal/grant.go", true},
		{"contain", "internal/contain.go", true},
		{"none", "internal/copy.go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, found := SecurityPacket([]string{tc.path})
			if found != tc.found || found && !reflect.DeepEqual(got, []string{tc.path}) || !found && len(got) != 0 {
				t.Fatalf("SecurityPacket() = %q, %v", got, found)
			}
		})
	}
}

func TestBuildRequestV3(t *testing.T) {
	t.Parallel()
	task := routing.PlanTask{
		TaskArtifact: routing.TaskArtifact{Title: "Change PublicFunction", Complexity: routing.ComplexityCritical, Domain: routing.DomainSecurity},
		Number:       1, Scope: []string{"contracts.go", "secret.go"}, Accept: []string{"PublicFunction changes"},
		Executor: "host-executor", Model: "host-model",
	}
	context := "**Task 1.** Own context.\nAPI_TOKEN=private-value\n\n**Task 2.** Other task."
	request, needed := BuildRequestV3(filepath.Join("testdata", "v3"), task, context)
	if !needed || request.Decision != "classify_v3" || len(request.Questions) != 2 {
		t.Fatalf("request = %#v, needed %v", request, needed)
	}
	for key, options := range map[string][]string{"contract": {"exported_change", "internal_only", "insufficient"}, "security": {"security_behaviour", "incidental", "insufficient"}} {
		question := request.Questions[key]
		criteria, ok := question.Criteria.(map[string]string)
		if question.Type != judge.QuestionChoice || !ok || len(criteria) != 3 || question.Instructions == "" {
			t.Fatalf("question %s = %#v", key, question)
		}
		for _, option := range options {
			if criteria[option] == "" {
				t.Fatalf("question %s missing %s", key, option)
			}
		}
	}
	state, err := json.Marshal(request.State)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"PublicFunction", "secret.go", "Own context"} {
		if !strings.Contains(string(state), value) {
			t.Fatalf("state missing %q: %s", value, state)
		}
	}
	for _, value := range []string{"critical", "security\"", "host-executor", "host-model", "private-value", "Other task"} {
		if strings.Contains(string(state), value) {
			t.Fatalf("state carries %q: %s", value, state)
		}
	}
	for _, tc := range []struct {
		name  string
		scope []string
		want  string
	}{
		{"contract only", []string{"contracts.go"}, "contract"},
		{"security only", []string{"secret.go"}, "security"},
		{"neither", []string{"other.go"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			selectedTask := task
			selectedTask.Scope = tc.scope
			got, needed := BuildRequestV3(filepath.Join("testdata", "v3"), selectedTask, context)
			if needed != (tc.want != "") || len(got.Questions) != len(strings.Fields(tc.want)) {
				t.Fatalf("BuildRequestV3() = %#v, %v", got, needed)
			}
			if tc.want != "" && got.Questions[tc.want].Type != judge.QuestionChoice {
				t.Fatalf("missing question %s", tc.want)
			}
		})
	}
}

func TestDecideV3Mapping(t *testing.T) {
	t.Parallel()
	firm := func(choice string) judge.Answer {
		return judge.Answer{Type: judge.QuestionChoice, Choice: choice, Confidence: 0.9}
	}
	for _, tc := range []struct {
		name    string
		code    routing.Complexity
		asked   map[string]bool
		answers map[string]judge.Answer
		want    routing.Complexity
	}{
		{"critical unchanged", routing.ComplexityCritical, map[string]bool{"contract": true}, map[string]judge.Answer{"contract": firm("exported_change")}, routing.ComplexityCritical},
		{"low raised", routing.ComplexityLow, map[string]bool{"contract": true}, map[string]judge.Answer{"contract": firm("exported_change")}, routing.ComplexityMedium},
		{"medium raised", routing.ComplexityMedium, map[string]bool{"security": true}, map[string]judge.Answer{"security": firm("security_behaviour")}, routing.ComplexityHigh},
		{"high capped", routing.ComplexityHigh, map[string]bool{"contract": true}, map[string]judge.Answer{"contract": firm("exported_change")}, routing.ComplexityHigh},
		{"both internal drops", routing.ComplexityHigh, map[string]bool{"contract": true, "security": true}, map[string]judge.Answer{"contract": firm("internal_only"), "security": firm("incidental")}, routing.ComplexityMedium},
		{"one internal drops", routing.ComplexityMedium, map[string]bool{"contract": true}, map[string]judge.Answer{"contract": firm("internal_only")}, routing.ComplexityLow},
		{"low floored", routing.ComplexityLow, map[string]bool{"security": true}, map[string]judge.Answer{"security": firm("incidental")}, routing.ComplexityLow},
		{"unasked stays", routing.ComplexityMedium, nil, nil, routing.ComplexityMedium},
		{"mixed raise wins", routing.ComplexityMedium, map[string]bool{"contract": true, "security": true}, map[string]judge.Answer{"contract": firm("internal_only"), "security": firm("security_behaviour")}, routing.ComplexityHigh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DecideV3(tc.code, tc.asked, tc.answers, 0.9)
			if got.Complexity != tc.want {
				t.Fatalf("DecideV3() = %#v, want %s", got, tc.want)
			}
		})
	}
}

func TestDecideV3Uncertainty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		asked  map[string]bool
		answer judge.Answer
		status string
	}{
		{"firm", map[string]bool{"contract": true}, judge.Answer{Type: judge.QuestionChoice, Choice: "exported_change", Confidence: 0.9}, "firm"},
		{"insufficient", map[string]bool{"contract": true}, judge.Answer{Type: judge.QuestionChoice, Choice: "insufficient", Confidence: 0.9}, "insufficient"},
		{"below threshold", map[string]bool{"contract": true}, judge.Answer{Type: judge.QuestionChoice, Choice: "exported_change", Confidence: 0.89}, "below_threshold"},
		{"unknown", map[string]bool{"contract": true}, judge.Answer{Type: judge.QuestionChoice, Choice: "unknown", Confidence: 0.9}, "unavailable"},
		{"wrong type", map[string]bool{"contract": true}, judge.Answer{Type: judge.QuestionNoul, Choice: "exported_change", Confidence: 0.9}, "unavailable"},
		{"missing", map[string]bool{"contract": true}, judge.Answer{}, "unavailable"},
		{"not asked", nil, judge.Answer{Type: judge.QuestionChoice, Choice: "exported_change", Confidence: 0.9}, "not_asked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answers := map[string]judge.Answer{"contract": tc.answer}
			if tc.name == "missing" {
				answers = nil
			}
			got := DecideV3(routing.ComplexityMedium, tc.asked, answers, 0.9)
			if got.Status["contract"] != tc.status {
				t.Fatalf("status = %q, want %q", got.Status["contract"], tc.status)
			}
			if tc.status != "firm" && got.Complexity != routing.ComplexityMedium {
				t.Fatalf("uncertain answer changed lane to %s", got.Complexity)
			}
		})
	}
}
