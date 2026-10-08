package council

import (
	"fmt"
	"slices"
	"strings"
)

type AggregatedFinding struct {
	Task     int      `json:"task"`
	Severity Severity `json:"severity"`
	Claim    string   `json:"claim"`
	Fix      string   `json:"fix"`
	IDs      []string `json:"ids"`
	Support  int      `json:"support"`
}

type Ranking struct {
	Label   string  `json:"label"`
	Average float64 `json:"average"`
	Count   int     `json:"count"`
}

type AggregateResult struct {
	Findings       []AggregatedFinding `json:"findings"`
	Rankings       []Ranking           `json:"rankings"`
	Recommendation Verdict             `json:"recommendation"`
}

type findingKey struct {
	task  int
	claim string
}

// Aggregate combines matching concerns and counts distinct counsellors' support.
func Aggregate(critiques []Critique, reviews []CrossReview) AggregateResult {
	type merged struct {
		finding    AggregatedFinding
		supporters map[string]bool
	}
	groups := make(map[findingKey]*merged)
	ids := make(map[string]findingKey)
	authors := make(map[string]string)
	labels := make(map[string]bool)
	result := AggregateResult{Recommendation: Approve}
	reviseVotes := 0
	for _, critique := range critiques {
		labels[critique.Label] = true
		if critique.Verdict == Revise {
			reviseVotes++
		}
		for i, finding := range critique.Findings {
			key := findingKey{finding.Task, normalizeClaim(finding.Claim)}
			group := groups[key]
			if group == nil {
				group = &merged{finding: AggregatedFinding{Task: finding.Task, Severity: finding.Severity, Claim: finding.Claim, Fix: finding.Fix}, supporters: make(map[string]bool)}
				groups[key] = group
			}
			if severityPriority(finding.Severity) > severityPriority(group.finding.Severity) {
				group.finding.Severity = finding.Severity
			}
			id := fmt.Sprintf("%s%d", critique.Label, i+1)
			group.finding.IDs = append(group.finding.IDs, id)
			group.supporters[critique.Label] = true
			ids[id] = key
			authors[id] = critique.Label
		}
	}
	for _, review := range reviews {
		if !labels[review.Reviewer] {
			continue
		}
		for id, agree := range review.Votes {
			key, exists := ids[id]
			if exists && agree && authors[id] != review.Reviewer {
				groups[key].supporters[review.Reviewer] = true
			}
		}
	}
	for _, group := range groups {
		group.finding.Support = len(group.supporters)
		slices.Sort(group.finding.IDs)
		result.Findings = append(result.Findings, group.finding)
		if group.finding.Severity == Blocker && group.finding.Support > len(critiques)/2 {
			result.Recommendation = Revise
		}
	}
	if reviseVotes > len(critiques)/2 {
		result.Recommendation = Revise
	}
	slices.SortFunc(result.Findings, func(a, b AggregatedFinding) int {
		if a.Task != b.Task {
			return a.Task - b.Task
		}
		return strings.Compare(normalizeClaim(a.Claim), normalizeClaim(b.Claim))
	})
	for _, critique := range critiques {
		rank := Ranking{Label: critique.Label}
		for _, review := range reviews {
			for i, label := range review.Ranking {
				if label == critique.Label && review.Reviewer != critique.Label {
					rank.Average += float64(i + 1)
					rank.Count++
				}
			}
		}
		if rank.Count > 0 {
			rank.Average /= float64(rank.Count)
		}
		result.Rankings = append(result.Rankings, rank)
	}
	slices.SortFunc(result.Rankings, func(a, b Ranking) int {
		if a.Count == 0 && b.Count != 0 {
			return 1
		}
		if a.Count != 0 && b.Count == 0 {
			return -1
		}
		if a.Average < b.Average {
			return -1
		}
		if a.Average > b.Average {
			return 1
		}
		return strings.Compare(a.Label, b.Label)
	})
	return result
}

func normalizeClaim(claim string) string {
	return strings.TrimSuffix(strings.ToLower(strings.Join(strings.Fields(claim), " ")), ".")
}

func severityPriority(severity Severity) int {
	switch severity {
	case Blocker:
		return 3
	case Major:
		return 2
	case Minor:
		return 1
	default:
		return 0
	}
}
