package classify

import (
	"strings"
	"testing"

	"github.com/batuta-ai/core/routing"
)

func TestOpenMarker(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		title   string
		accept  []string
		number  int
		context string
		want    bool
	}{
		{"plain title, no marker", "Add the retry helper", nil, 1, "", false},
		{"title question mark", "Should this retry?", nil, 1, "", true},
		{"title TBD whole word", "Wire the timeout TBD", nil, 1, "", true},
		{"title TBD substring not matched", "Wire the TBDoubler helper", nil, 1, "", false},
		{"title lowercase to be decided", "Naming is to be decided", nil, 1, "", true},
		{"accept entry marker", "Add the retry helper", []string{"behavior is undecided"}, 1, "", true},
		{"accept entry no marker", "Add the retry helper", []string{"tests pass"}, 1, "", false},
		{"context marker for this task", "Add the retry helper", nil, 2, "**Task 2.** Open question: which backend?", true},
		{"context marker for other task only", "Add the retry helper", nil, 1, "**Task 2.** Open question: which backend?", false},
		{"context marker case-insensitive", "Add the retry helper", nil, 1, "**Task 1.** DECIDE WHETHER to cache.", true},
		{"open decision marker", "Add the retry helper", []string{"open decision: format"}, 1, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			task := routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{Title: tc.title},
				Number:       tc.number,
				Accept:       tc.accept,
			}
			if got := OpenMarker(task, tc.context); got != tc.want {
				t.Fatalf("OpenMarker() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRuleV3Validate(t *testing.T) {
	t.Parallel()

	if DefaultRuleV3 != (RuleV3{FHigh: 6, FLow: 2, DocsLow: false, Threshold: 0.9}) {
		t.Fatalf("DefaultRuleV3 = %#v", DefaultRuleV3)
	}
	if err := DefaultRuleV3.Validate(); err != nil {
		t.Fatalf("DefaultRuleV3.Validate() = %v", err)
	}

	for _, tc := range []struct {
		name string
		rule RuleV3
		ok   bool
	}{
		{"default", DefaultRuleV3, true},
		{"FHigh lowest", RuleV3{FHigh: 4, FLow: 1, DocsLow: false, Threshold: 0.7}, true},
		{"FHigh highest", RuleV3{FHigh: 8, FLow: 2, DocsLow: true, Threshold: 0.8}, true},
		{"FHigh outside grid", RuleV3{FHigh: 3, FLow: 1, DocsLow: false, Threshold: 0.7}, false},
		{"FHigh outside grid high", RuleV3{FHigh: 9, FLow: 1, DocsLow: false, Threshold: 0.7}, false},
		{"FLow outside grid", RuleV3{FHigh: 6, FLow: 3, DocsLow: false, Threshold: 0.7}, false},
		{"FLow zero", RuleV3{FHigh: 6, FLow: 0, DocsLow: false, Threshold: 0.7}, false},
		{"Threshold outside grid", RuleV3{FHigh: 6, FLow: 2, DocsLow: false, Threshold: 0.5}, false},
		{"Threshold outside grid high", RuleV3{FHigh: 6, FLow: 2, DocsLow: false, Threshold: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.rule.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
		})
	}
}

func TestCodeLaneV3(t *testing.T) {
	t.Parallel()

	openTask := routing.PlanTask{TaskArtifact: routing.TaskArtifact{Title: "Wire the timeout TBD"}}
	plainTask := routing.PlanTask{TaskArtifact: routing.TaskArtifact{Title: "Add the retry helper"}}

	for _, tc := range []struct {
		name     string
		task     routing.PlanTask
		context  string
		features ScopeFeatures
		rule     RuleV3
		want     routing.Complexity
	}{
		{"open marker wins over everything", openTask, "", ScopeFeatures{Files: 1, Directories: 1}, DefaultRuleV3, routing.ComplexityCritical},
		{"files at FHigh is high", plainTask, "", ScopeFeatures{Files: 6, Directories: 1}, DefaultRuleV3, routing.ComplexityHigh},
		{"files above FHigh is high", plainTask, "", ScopeFeatures{Files: 7, Directories: 1}, DefaultRuleV3, routing.ComplexityHigh},
		{"directories at 3 is high", plainTask, "", ScopeFeatures{Files: 1, Directories: 3}, DefaultRuleV3, routing.ComplexityHigh},
		{"files at FLow is low", plainTask, "", ScopeFeatures{Files: 2, Directories: 1}, DefaultRuleV3, routing.ComplexityLow},
		{"files below FLow is low", plainTask, "", ScopeFeatures{Files: 1, Directories: 1}, DefaultRuleV3, routing.ComplexityLow},
		{"docs-only under FLow with DocsLow false is medium", plainTask, "", ScopeFeatures{Files: 1, Directories: 1, DocsOnly: true}, DefaultRuleV3, routing.ComplexityMedium},
		{"docs-only under FLow with DocsLow true is low", plainTask, "", ScopeFeatures{Files: 1, Directories: 1, DocsOnly: true}, RuleV3{FHigh: 6, FLow: 2, DocsLow: true, Threshold: 0.9}, routing.ComplexityLow},
		{"between FLow and FHigh is medium", plainTask, "", ScopeFeatures{Files: 3, Directories: 1}, DefaultRuleV3, routing.ComplexityMedium},
		{"high beats low when both thresholds hit by directories", plainTask, "", ScopeFeatures{Files: 1, Directories: 3}, DefaultRuleV3, routing.ComplexityHigh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := CodeLaneV3(tc.task, tc.context, tc.features, tc.rule)
			if got != tc.want {
				t.Fatalf("CodeLaneV3() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSplitV3(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		slug string
		byte byte
		want string
	}{
		{"classify-v3", 0xec, "calibrate"},
		{"classify-v2", 0xc3, "test"},
		{"watch", 0xba, "calibrate"},
		{"roadmap", 0x3d, "test"},
		{"dashboard", 0x66, "calibrate"},
		{"review", 0xc9, "test"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			t.Parallel()
			if got := SplitV3(tc.slug); got != tc.want {
				t.Fatalf("SplitV3(%q) = %q, want %q (byte 0x%02x)", tc.slug, got, tc.want, tc.byte)
			}
		})
	}

	counts := map[string]int{"calibrate": 0, "test": 0}
	for i := 0; i < 200; i++ {
		slug := "plan-" + strings.Repeat("x", i%7) + string(rune('a'+i%26))
		split := SplitV3(slug)
		if split != "calibrate" && split != "test" {
			t.Fatalf("SplitV3(%q) = %q, want calibrate or test", slug, split)
		}
		counts[split]++
		if got := SplitV3(slug); got != split {
			t.Fatalf("SplitV3(%q) not stable: %q then %q", slug, split, got)
		}
	}
	if counts["calibrate"] == 0 || counts["test"] == 0 {
		t.Fatalf("SplitV3 never varied across %d slugs: %#v", 200, counts)
	}
}
