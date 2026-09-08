package review

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func testFinding(severity Severity) Finding {
	return Finding{Severity: severity, Kind: Defect, File: "src/main.go", Line: 12, Premise: "Concurrent read races.", Path: "Read calls cache while Write mutates it", Verdict: "Concurrent calls race", Fix: "Protect the cache", Rule: "race"}
}

func findingJSON(t *testing.T, f Finding) string {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseFindings(t *testing.T) {
	defect := testFinding(Blocker)
	advisory := testFinding(Minor)
	advisory.Kind, advisory.Verdict = Advisory, ""
	advisory.Path = "Name the helper after its effect"
	text := "<<<REPORT\nsummary\nREPORT>>>\n<<<FINDINGS\r\n" + findingJSON(t, defect) + "\r\n\n" + findingJSON(t, advisory) + "\nFINDINGS>>>\ntrailer"
	got, rejected := ParseFindings(text)
	if len(rejected) != 0 || !reflect.DeepEqual(got, []Finding{defect, advisory}) {
		t.Fatalf("ParseFindings = %+v, %+v", got, rejected)
	}
	got, rejected = ParseFindings("<<<FINDINGS\nFINDINGS>>>")
	if len(got) != 0 || len(rejected) != 0 {
		t.Fatalf("empty findings block = %+v, %+v", got, rejected)
	}
	large := testFinding(Nit)
	large.Premise = strings.Repeat("a", 70*1024)
	got, rejected = ParseFindings("<<<FINDINGS\n" + findingJSON(t, large) + "\nFINDINGS>>>")
	if len(got) != 1 || len(rejected) != 0 {
		t.Fatal("valid line was silently truncated")
	}
}

func TestParseFindingsRejectsMalformed(t *testing.T) {
	valid := findingJSON(t, testFinding(Major))
	cases := map[string]string{
		"syntax": "{", "null": "null", "unknown field": strings.Replace(valid, `"rule":`, `"surprise":`, 1),
		"severity":        strings.Replace(valid, `"major"`, `"critical"`, 1),
		"kind":            strings.Replace(valid, `"defect"`, `"bug"`, 1),
		"line":            strings.Replace(valid, `"line":12`, `"line":0`, 1),
		"fraction":        strings.Replace(valid, `"line":12`, `"line":1.5`, 1),
		"range":           strings.Replace(valid, `"line":12`, `"line":12,"end_line":11`, 1),
		"path escape":     strings.Replace(valid, "src/main.go", "../secret", 1),
		"absolute path":   strings.Replace(valid, "src/main.go", "/tmp/secret", 1),
		"premise":         strings.Replace(valid, "Concurrent read races.", "  ", 1),
		"path":            strings.Replace(valid, "Read calls cache while Write mutates it", "", 1),
		"verdict":         strings.Replace(valid, "Concurrent calls race", "", 1),
		"advisory fix":    strings.Replace(strings.Replace(valid, `"defect"`, `"advisory"`, 1), "Protect the cache", "", 1),
		"trailing JSON":   valid + " {}",
		"duplicate field": strings.Replace(valid, `"line":12`, `"line":1,"line":12`, 1),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			got, rejected := ParseFindings("<<<FINDINGS\n" + bad + "\n" + valid + "\nFINDINGS>>>")
			if len(got) != 1 || len(rejected) != 1 || rejected[0].Line != 2 || rejected[0].Reason == "" || rejected[0].Text != bad {
				t.Fatalf("got %+v, rejected %+v", got, rejected)
			}
		})
	}
	for _, text := range []string{valid, "<<<FINDINGS\n" + valid, "FINDINGS>>>", "<<<FINDINGS\n<<<FINDINGS\nFINDINGS>>>", "<<<FINDINGS\nFINDINGS>>>\n<<<FINDINGS\nFINDINGS>>>"} {
		got, rejected := ParseFindings(text)
		if len(got) != 0 || len(rejected) == 0 || rejected[0].Reason == "" {
			t.Errorf("accepted invalid framing %q: %+v, %+v", text, got, rejected)
		}
	}
}

func TestMergeDeduplicates(t *testing.T) {
	a := testFinding(Minor)
	b := testFinding(Blocker)
	b.File, b.Premise, b.EndLine = "./src/main.go", "  concurrent   READ races  ", b.Line
	c := testFinding(Major)
	c.EndLine = 14
	d := testFinding(Minor)
	d.Premise = "A distinct problem"
	input := []Finding{a, c, b, d}
	before := append([]Finding(nil), input...)
	got := Merge(input, nil)
	if len(got.Findings) != 3 || len(got.Suppressed) != 0 || !reflect.DeepEqual(input, before) {
		t.Fatalf("merge = %+v, input = %+v", got, input)
	}
	blockers := 0
	for _, f := range got.Findings {
		if f.Severity == Blocker {
			blockers++
		}
	}
	if blockers != 1 {
		t.Fatalf("highest severity was lost: %+v", got)
	}
	if reverse := Merge([]Finding{d, b, c, a}, nil); !reflect.DeepEqual(got, reverse) {
		t.Fatalf("merge depends on arrival order: %+v != %+v", got, reverse)
	}
	if twice := Merge(got.Findings, nil); !reflect.DeepEqual(got, twice) {
		t.Fatal("merge is not idempotent")
	}
}

func TestMergeSuppressesLinterOverlap(t *testing.T) {
	a := testFinding(Major)
	a.EndLine = 15
	b := a
	b.Rule, b.Premise = "another-rule", "Another defect on the same line"
	c := a
	c.Line, c.EndLine = 20, 22
	d := a
	d.File = "other.go"
	linters := []LinterFinding{{File: "./src/main.go", Line: 14, EndLine: 18, Rule: "race"}}
	got := Merge([]Finding{a, b, c, d}, linters)
	if len(got.Findings) != 3 || len(got.Suppressed) != 1 || got.Suppressed[0].Finding.Premise != a.Premise || got.Suppressed[0].Reason == "" || got.Suppressed[0].Linter.Rule != "race" {
		t.Fatalf("linter overlap = %+v", got)
	}
	unknown := a
	unknown.Rule = ""
	if got := Merge([]Finding{unknown}, []LinterFinding{{File: a.File, Line: a.Line}}); len(got.Suppressed) != 0 {
		t.Fatal("same location without a shared rule does not prove linter overlap")
	}
}
