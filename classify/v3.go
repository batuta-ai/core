package classify

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/batuta-ai/core/routing"
)

var (
	openMarkers  = []string{"TBD", "to be decided", "to be defined", "undecided", "open question", "open decision", "decide whether"}
	openMarkerRe = regexp.MustCompile(`(?i)\bTBD\b`)
)

// OpenMarker reports whether the task's title, an Accept entry or its own
// bounded context paragraph names one of the frozen open markers of
// judge-research.md section 14, or the title contains a question mark.
func OpenMarker(task routing.PlanTask, context string) bool {
	if strings.Contains(task.Title, "?") {
		return true
	}
	if containsOpenMarker(task.Title) {
		return true
	}
	for _, entry := range task.Accept {
		if containsOpenMarker(entry) {
			return true
		}
	}
	return containsOpenMarker(boundContext(task.Number, context))
}

func containsOpenMarker(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range openMarkers {
		if marker == "TBD" {
			if openMarkerRe.MatchString(text) {
				return true
			}
			continue
		}
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// RuleV3 carries the parameters selected by the calibrate procedure of
// judge-research.md section 14: the code lane's file thresholds and
// docs-only tie-break, and the Jev firmness threshold.
type RuleV3 struct {
	FHigh     int
	FLow      int
	DocsLow   bool
	Threshold float64
}

// DefaultRuleV3 is the rule used before calibration selects one.
var DefaultRuleV3 = RuleV3{FHigh: 6, FLow: 2, DocsLow: false, Threshold: 0.9}

var (
	ruleV3FHighGrid     = []int{4, 5, 6, 7, 8}
	ruleV3FLowGrid      = []int{1, 2}
	ruleV3ThresholdGrid = []float64{0.7, 0.8, 0.9}
)

// Validate rejects any RuleV3 value outside the frozen grids of section 14.
func (r RuleV3) Validate() error {
	if !slices.Contains(ruleV3FHighGrid, r.FHigh) {
		return fmt.Errorf("classify: FHigh %d outside frozen grid %v", r.FHigh, ruleV3FHighGrid)
	}
	if !slices.Contains(ruleV3FLowGrid, r.FLow) {
		return fmt.Errorf("classify: FLow %d outside frozen grid %v", r.FLow, ruleV3FLowGrid)
	}
	if !slices.Contains(ruleV3ThresholdGrid, r.Threshold) {
		return fmt.Errorf("classify: Threshold %v outside frozen grid %v", r.Threshold, ruleV3ThresholdGrid)
	}
	return nil
}

// CodeLaneV3 applies the frozen first-match order of section 14's lane C:
// critical on the open marker, high on files at least FHigh or directories
// at least 3, low on files at most FLow unless the task is docs-only and
// DocsLow is false, otherwise medium.
func CodeLaneV3(task routing.PlanTask, context string, features ScopeFeatures, rule RuleV3) routing.Complexity {
	switch {
	case OpenMarker(task, context):
		return routing.ComplexityCritical
	case features.Files >= rule.FHigh || features.Directories >= 3:
		return routing.ComplexityHigh
	case features.Files <= rule.FLow && !(features.DocsOnly && !rule.DocsLow):
		return routing.ComplexityLow
	default:
		return routing.ComplexityMedium
	}
}

// SplitV3 returns "calibrate" when the first byte of sha256 of the plan
// slug is even, "test" when it is odd, as frozen in section 14.
func SplitV3(planSlug string) string {
	sum := sha256.Sum256([]byte(planSlug))
	if sum[0]%2 == 0 {
		return "calibrate"
	}
	return "test"
}
