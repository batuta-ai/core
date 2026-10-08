package council

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Severity string

const (
	Blocker Severity = "blocker"
	Major   Severity = "major"
	Minor   Severity = "minor"
)

type Verdict string

const (
	Approve Verdict = "APPROVE"
	Revise  Verdict = "REVISE"
)

type Finding struct {
	Task     int      `json:"task"`
	Severity Severity `json:"severity"`
	Claim    string   `json:"claim"`
	Fix      string   `json:"fix"`
}

type Critique struct {
	Label    string    `json:"label"`
	Findings []Finding `json:"findings"`
	Verdict  Verdict   `json:"verdict"`
}

type CrossReview struct {
	Reviewer string          `json:"reviewer"`
	Votes    map[string]bool `json:"votes"`
	Ranking  []string        `json:"ranking"`
}

// ParseCritique accepts one complete findings block followed by one verdict.
func ParseCritique(output, label string) (Critique, error) {
	if !validLabel(label) {
		return Critique{}, fmt.Errorf("council: invalid critique label %q", label)
	}
	lines := outputLines(output)
	if len(lines) < 3 || lines[0] != "<<<COUNCIL" {
		return Critique{}, fmt.Errorf("council: missing COUNCIL block")
	}
	close := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "COUNCIL>>>" {
			close = i
			break
		}
		if lines[i] == "<<<COUNCIL" {
			return Critique{}, fmt.Errorf("council: nested COUNCIL block")
		}
	}
	if close < 0 {
		return Critique{}, fmt.Errorf("council: missing or malformed COUNCIL closing marker")
	}
	var verdict Verdict
	for _, line := range lines[close+1:] {
		if line == "<<<COUNCIL" || line == "COUNCIL>>>" {
			return Critique{}, fmt.Errorf("council: duplicate COUNCIL block")
		}
		if !strings.HasPrefix(line, "VERDICT:") {
			continue
		}
		verdictText, ok := strings.CutPrefix(line, "VERDICT: ")
		if !ok || Verdict(verdictText) != Approve && Verdict(verdictText) != Revise || verdict != "" {
			return Critique{}, fmt.Errorf("council: verdict must be APPROVE or REVISE exactly once")
		}
		verdict = Verdict(verdictText)
	}
	if verdict == "" {
		return Critique{}, fmt.Errorf("council: verdict must be APPROVE or REVISE")
	}
	critique := Critique{Label: label, Verdict: verdict}
	for _, line := range lines[1:close] {
		finding, err := parseCouncilFinding(line)
		if err != nil {
			return Critique{}, err
		}
		critique.Findings = append(critique.Findings, finding)
	}
	return critique, nil
}

func parseCouncilFinding(line string) (Finding, error) {
	var finding Finding
	decoder := json.NewDecoder(strings.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return finding, fmt.Errorf("council: finding must be one JSON object per line")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return finding, fmt.Errorf("council: invalid finding JSON: %w", err)
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return finding, fmt.Errorf("council: duplicate or invalid finding field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return finding, fmt.Errorf("council: invalid finding JSON: %w", err)
		}
		if string(value) == "null" {
			return finding, fmt.Errorf("council: finding field %q must not be null", key)
		}
		switch key {
		case "task":
			err = json.Unmarshal(value, &finding.Task)
		case "severity":
			err = json.Unmarshal(value, &finding.Severity)
		case "claim":
			err = json.Unmarshal(value, &finding.Claim)
		case "fix":
			err = json.Unmarshal(value, &finding.Fix)
		default:
			return finding, fmt.Errorf("council: unknown finding field %q", key)
		}
		if err != nil {
			return finding, fmt.Errorf("council: invalid finding field %q: %w", key, err)
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return finding, fmt.Errorf("council: malformed finding object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return finding, fmt.Errorf("council: expected one JSON object per line")
	}
	if len(seen) != 4 || finding.Task < 1 || finding.Severity != Blocker && finding.Severity != Major && finding.Severity != Minor || strings.TrimSpace(finding.Claim) == "" || strings.TrimSpace(finding.Fix) == "" {
		return finding, fmt.Errorf("council: finding requires positive task, blocker|major|minor severity, claim and fix")
	}
	return finding, nil
}

// ParseCrossReview validates every vote and a complete permutation of the other labels.
func ParseCrossReview(output string, critiques []Critique, ownLabel string) (CrossReview, error) {
	if !validLabel(ownLabel) {
		return CrossReview{}, fmt.Errorf("council: invalid reviewer label %q", ownLabel)
	}
	expectedIDs := make(map[string]bool)
	expectedLabels := make(map[string]bool)
	ownFound := false
	for _, critique := range critiques {
		if !validLabel(critique.Label) || expectedLabels[critique.Label] || critique.Label == ownLabel && ownFound {
			return CrossReview{}, fmt.Errorf("council: invalid or duplicate critique label %q", critique.Label)
		}
		if critique.Label == ownLabel {
			ownFound = true
			continue
		}
		expectedLabels[critique.Label] = true
		for i := range critique.Findings {
			expectedIDs[fmt.Sprintf("%s%d", critique.Label, i+1)] = true
		}
	}
	if !ownFound || len(expectedLabels) == 0 {
		return CrossReview{}, fmt.Errorf("council: reviewer and other critiques are required")
	}
	lines := outputLines(output)
	result := CrossReview{Reviewer: ownLabel, Votes: make(map[string]bool)}
	ranking := false
	rankingEnded := false
	for _, line := range lines {
		if line == "FINAL RANKING:" {
			if ranking {
				return CrossReview{}, fmt.Errorf("council: duplicate ranking")
			}
			ranking = true
			continue
		}
		if !ranking {
			id, vote, hasColon := strings.Cut(line, ":")
			if !hasColon {
				fields := strings.Fields(line)
				if len(fields) > 1 && isFindingID(fields[0]) && (fields[1] == "AGREE" || fields[1] == "DISAGREE") {
					return CrossReview{}, fmt.Errorf("council: unknown finding id or invalid vote %q", line)
				}
				continue
			}
			if !isFindingID(strings.TrimSpace(id)) {
				continue
			}
			if !expectedIDs[id] || vote != " AGREE" && vote != " DISAGREE" {
				return CrossReview{}, fmt.Errorf("council: unknown finding id or invalid vote %q", line)
			}
			if _, exists := result.Votes[id]; exists {
				return CrossReview{}, fmt.Errorf("council: repeated vote for %s", id)
			}
			result.Votes[id] = vote == " AGREE"
			continue
		}
		if rankingEnded {
			continue
		}
		if !looksLikeRankingEntry(line) {
			rankingEnded = true
			continue
		}
		position, label, ok := strings.Cut(line, ". ")
		index, err := strconv.Atoi(position)
		label = strings.TrimPrefix(label, "Critique ")
		if !ok || err != nil || index != len(result.Ranking)+1 || !expectedLabels[label] {
			return CrossReview{}, fmt.Errorf("council: invalid ranking entry %q", line)
		}
		for _, prior := range result.Ranking {
			if prior == label {
				return CrossReview{}, fmt.Errorf("council: repeated ranking label %s", label)
			}
		}
		result.Ranking = append(result.Ranking, label)
	}
	if !ranking || len(result.Votes) != len(expectedIDs) || len(result.Ranking) != len(expectedLabels) {
		return CrossReview{}, fmt.Errorf("council: incomplete votes or ranking")
	}
	return result, nil
}

func isFindingID(id string) bool {
	i := 0
	for i < len(id) && id[i] >= 'A' && id[i] <= 'Z' {
		i++
	}
	if i == 0 || i == len(id) {
		return false
	}
	for ; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

func looksLikeRankingEntry(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i < len(line) && (line[i] == '.' || line[i] == ')')
}

func outputLines(output string) []string {
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(output, "\r\n", "\n"), "\n"), "\n")
}

func validLabel(label string) bool {
	if label == "" {
		return false
	}
	for _, r := range label {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
