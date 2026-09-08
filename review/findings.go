package review

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

type Severity string

const (
	Blocker Severity = "blocker"
	Major   Severity = "major"
	Minor   Severity = "minor"
	Nit     Severity = "nit"
)

type Kind string

const (
	Defect   Kind = "defect"
	Advisory Kind = "advisory"
)

type Finding struct {
	Severity Severity `json:"severity"`
	Kind     Kind     `json:"kind"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	EndLine  int      `json:"end_line,omitempty"` // Inclusive; zero means Line.
	Premise  string   `json:"premise"`
	Path     string   `json:"path"` // Execution path for defects; improvement for advisories.
	Verdict  string   `json:"verdict"`
	Fix      string   `json:"fix"`
	Rule     string   `json:"rule"`
}

type RejectedLine struct {
	Line   int    `json:"line"` // One-based line in the complete reviewer output.
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

// ParseFindings accepts exactly one complete marker block. Malformed framing
// rejects the entire block; malformed JSON lines do not discard valid neighbours.
func ParseFindings(output string) ([]Finding, []RejectedLine) {
	lines := strings.Split(output, "\n")
	start, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case "<<<FINDINGS":
			if start != -1 || end != -1 {
				return nil, []RejectedLine{{Line: i + 1, Text: line, Reason: "duplicate or nested FINDINGS block"}}
			}
			start = i
		case "FINDINGS>>>":
			if start == -1 || end != -1 {
				return nil, []RejectedLine{{Line: i + 1, Text: line, Reason: "unexpected FINDINGS closing marker"}}
			}
			end = i
		}
	}
	if start == -1 || end == -1 {
		return nil, []RejectedLine{{Line: len(lines), Reason: "missing or unterminated FINDINGS block"}}
	}
	var findings []Finding
	var rejected []RejectedLine
	for i := start + 1; i < end; i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		finding, err := parseFinding(line)
		if err != nil {
			rejected = append(rejected, RejectedLine{Line: i + 1, Text: line, Reason: err.Error()})
			continue
		}
		findings = append(findings, finding)
	}
	return findings, rejected
}

func parseFinding(line string) (Finding, error) {
	var finding Finding
	if err := uniqueFields(line); err != nil {
		return finding, err
	}
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&finding); err != nil {
		return finding, fmt.Errorf("invalid finding JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return finding, fmt.Errorf("expected one JSON object per line")
	}
	if severityRank(finding.Severity) == 0 {
		return finding, fmt.Errorf("severity must be blocker, major, minor or nit")
	}
	if finding.Kind != Defect && finding.Kind != Advisory {
		return finding, fmt.Errorf("kind must be defect or advisory")
	}
	if !validPath(finding.File) {
		return finding, fmt.Errorf("file must be a repository-relative path")
	}
	if finding.Line < 1 || finding.EndLine < 0 || finding.EndLine != 0 && finding.EndLine < finding.Line {
		return finding, fmt.Errorf("line must be positive and end_line must not precede line")
	}
	if strings.TrimSpace(finding.Premise) == "" || strings.TrimSpace(finding.Path) == "" {
		return finding, fmt.Errorf("premise and path (or advisory improvement) are required")
	}
	if finding.Kind == Defect && strings.TrimSpace(finding.Verdict) == "" {
		return finding, fmt.Errorf("defect requires a verdict")
	}
	if finding.Kind == Advisory && strings.TrimSpace(finding.Fix) == "" {
		return finding, fmt.Errorf("advisory requires a fix")
	}
	return finding, nil
}

func uniqueFields(line string) error {
	decoder := json.NewDecoder(strings.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("finding must be a JSON object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid finding JSON: %w", err)
		}
		name, ok := token.(string)
		if !ok {
			return fmt.Errorf("invalid finding field")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("duplicate finding field %q", name)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("invalid finding JSON: %w", err)
		}
		if string(value) == "null" {
			return fmt.Errorf("field %q must not be null", name)
		}
	}
	return nil
}

type LinterFinding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
	Rule    string `json:"rule"`
}

type SuppressedFinding struct {
	Finding Finding       `json:"finding"`
	Linter  LinterFinding `json:"linter"`
	Reason  string        `json:"reason"`
}

type MergeResult struct {
	Findings   []Finding           `json:"findings"`
	Suppressed []SuppressedFinding `json:"suppressed"`
}

// Merge consumes accepted findings. Suppression requires a shared nonempty rule
// and intersecting locations; proximity alone does not establish linter overlap.
// Inputs are never mutated and arrival order does not affect the result.
func Merge(findings []Finding, linters []LinterFinding) MergeResult {
	type key struct {
		file    string
		start   int
		end     int
		problem string
	}
	unique := make(map[key]Finding)
	for _, finding := range findings {
		finding.File = path.Clean(finding.File)
		finding.EndLine = rangeEnd(finding.Line, finding.EndLine)
		problem := strings.TrimRight(strings.ToLower(strings.Join(strings.Fields(finding.Premise), " ")), ".")
		k := key{finding.File, finding.Line, finding.EndLine, problem}
		prior, exists := unique[k]
		if !exists || severityRank(finding.Severity) > severityRank(prior.Severity) || severityRank(finding.Severity) == severityRank(prior.Severity) && findingOrder(finding, prior) < 0 {
			unique[k] = finding
		}
	}
	ordered := make([]Finding, 0, len(unique))
	for _, finding := range unique {
		ordered = append(ordered, finding)
	}
	slices.SortFunc(ordered, findingOrder)
	checks := append([]LinterFinding(nil), linters...)
	for i := range checks {
		checks[i].File = path.Clean(checks[i].File)
		checks[i].EndLine = rangeEnd(checks[i].Line, checks[i].EndLine)
	}
	slices.SortFunc(checks, func(a, b LinterFinding) int {
		left, _ := json.Marshal(a)
		right, _ := json.Marshal(b)
		return strings.Compare(string(left), string(right))
	})
	var result MergeResult
	for _, finding := range ordered {
		suppressed := false
		for _, check := range checks {
			if finding.Rule != "" && finding.Rule == check.Rule && finding.File == check.File && check.Line > 0 && check.EndLine >= check.Line && finding.Line <= check.EndLine && check.Line <= finding.EndLine {
				result.Suppressed = append(result.Suppressed, SuppressedFinding{Finding: finding, Linter: check, Reason: "linter overlap: " + check.Rule})
				suppressed = true
				break
			}
		}
		if !suppressed {
			result.Findings = append(result.Findings, finding)
		}
	}
	return result
}

func findingOrder(a, b Finding) int {
	if order := strings.Compare(a.File, b.File); order != 0 {
		return order
	}
	if a.Line < b.Line {
		return -1
	}
	if a.Line > b.Line {
		return 1
	}
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return strings.Compare(string(left), string(right))
}

func rangeEnd(start, end int) int {
	if end == 0 {
		return start
	}
	return end
}

func severityRank(severity Severity) int {
	switch severity {
	case Blocker:
		return 4
	case Major:
		return 3
	case Minor:
		return 2
	case Nit:
		return 1
	default:
		return 0
	}
}
