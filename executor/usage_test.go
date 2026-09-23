package executor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestUsageMissingCountersRemainUnknown(t *testing.T) {
	t.Parallel()
	usage := Usage{Provenance: "acp/session-update"}
	payload, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"provenance":"acp/session-update"}` {
		t.Fatalf("json.Marshal() = %s", payload)
	}
	if total, known := usage.TotalTokens(); known || total != 0 {
		t.Fatalf("TotalTokens() = %d, %t; want unknown", total, known)
	}
}

func TestUsageExtendedFieldsUnknownByDefault(t *testing.T) {
	t.Parallel()
	usage := Usage{Provenance: "acp/session-update"}
	payload, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"provenance":"acp/session-update"}` {
		t.Fatalf("json.Marshal() = %s", payload)
	}
	if usage.CacheReadTokens != nil || usage.CacheWriteTokens != nil || usage.ReasoningTokens != nil ||
		usage.ReportedTotalTokens != nil || usage.CostAmount != nil || usage.CostCurrency != "" ||
		usage.CacheSemantics != "" {
		t.Fatalf("extended usage fields set by default: %#v", usage)
	}
}

func TestUsageTotalDoesNotDoubleCountCachedInput(t *testing.T) {
	t.Parallel()
	input, cached, output := int64(100), int64(40), int64(25)
	usage := Usage{
		InputTokens:       &input,
		CachedInputTokens: &cached,
		OutputTokens:      &output,
		Provenance:        "worker/final-result",
	}
	if total, known := usage.TotalTokens(); !known || total != 125 {
		t.Fatalf("TotalTokens() = %d, %t; want 125, true", total, known)
	}
	if usage.Provenance != "worker/final-result" {
		t.Fatalf("provenance changed: %q", usage.Provenance)
	}
}

func TestUsageTotalBySemantics(t *testing.T) {
	t.Parallel()
	input, output, cacheRead, cacheWrite := int64(100), int64(25), int64(40), int64(10)
	cost := 50.0
	additive := Usage{
		InputTokens:      &input,
		OutputTokens:     &output,
		CacheReadTokens:  &cacheRead,
		CacheWriteTokens: &cacheWrite,
		CostAmount:       &cost,
		CostCurrency:     "USD",
		CacheSemantics:   CacheSemanticsAdditive,
		Provenance:       "acp/session-update",
	}
	if total, known := additive.TotalTokens(); !known || total != 175 {
		t.Fatalf("additive TotalTokens() = %d, %t; want 175, true", total, known)
	}

	// An additive total adds only the cache counters it was given.
	partial := additive
	partial.CacheWriteTokens = nil
	if total, known := partial.TotalTokens(); !known || total != 165 {
		t.Fatalf("additive partial TotalTokens() = %d, %t; want 165, true", total, known)
	}

	// A negative cache counter leaves the additive total unknown.
	negative := cacheWrite
	negative = -negative
	partial = additive
	partial.CacheWriteTokens = &negative
	if total, known := partial.TotalTokens(); known || total != 0 {
		t.Fatalf("additive negative TotalTokens() = %d, %t; want unknown", total, known)
	}

	// Subset and unset semantics never add the cached counters, and cost
	// never enters the total.
	subset := Usage{
		InputTokens:       &input,
		CachedInputTokens: &cacheRead,
		OutputTokens:      &output,
		CacheReadTokens:   &cacheRead,
		CacheWriteTokens:  &cacheWrite,
		CostAmount:        &cost,
		CacheSemantics:    CacheSemanticsSubset,
		Provenance:        "worker/final-result",
	}
	for _, usage := range []Usage{subset, func() Usage { unset := subset; unset.CacheSemantics = ""; return unset }()} {
		if total, known := usage.TotalTokens(); !known || total != 125 {
			t.Fatalf("subset TotalTokens() = %d, %t for %#v; want 125, true", total, known, usage)
		}
	}

	// A missing input or output leaves the total unknown under both
	// semantics.
	orphan := additive
	orphan.InputTokens = nil
	for _, usage := range []Usage{orphan, func() Usage { noOut := additive; noOut.OutputTokens = nil; return noOut }(), func() Usage { noIn := subset; noIn.InputTokens = nil; return noIn }()} {
		if total, known := usage.TotalTokens(); known || total != 0 {
			t.Fatalf("missing counter TotalTokens() = %d, %t for %#v; want unknown", total, known, usage)
		}
	}
}

func TestUsageTotalRequiresInputAndOutput(t *testing.T) {
	t.Parallel()
	input, cached, output := int64(10), int64(7), int64(3)
	for _, usage := range []Usage{
		{InputTokens: &input},
		{OutputTokens: &output},
		{CachedInputTokens: &cached, OutputTokens: &output},
	} {
		if total, known := usage.TotalTokens(); known || total != 0 {
			t.Fatalf("TotalTokens() = %d, %t for %#v; want unknown", total, known, usage)
		}
	}
}

func TestOutcomeUsageFromTail(t *testing.T) {
	t.Parallel()
	adapter := Adapter{UsageRegex: "input:\\s*(?P<input>[0-9][0-9,. ]*)\\s+output:\\s*(?P<output>[0-9][0-9,. ]*)\\s+cached:\\s*(?P<cached>[0-9][0-9,. \u2009]*)"}
	var stdout strings.Builder
	// A decoy with different counters sits in the prefix the 20-line tail
	// drops: the leftmost match wins, so extracting the decoy's counters
	// means more than the tail was read.
	stdout.WriteString("USAGE — INPUT: 1 OUTPUT: 1 CACHED: 1\n")
	for line := 1; line <= 25; line++ {
		fmt.Fprintf(&stdout, "noise %d\n", line)
	}
	// The usage line is on the last line of the stdout tail and uses a
	// comma, a dot and a thin space as thousands separators; the keywords
	// are upper-case while the pattern is lower-case.
	stdout.WriteString("USAGE — INPUT: 1,234,567 OUTPUT: 2.500 CACHED: 1\u2009000\n")
	result := Result{ExitCode: 0, Stdout: []byte(stdout.String())}
	adapter.Outcome(&result)
	if result.Usage == nil {
		t.Fatal("Outcome() did not fill Usage")
	}
	usage := *result.Usage
	if usage.InputTokens == nil || *usage.InputTokens != 1234567 {
		t.Fatalf("input = %v, want 1234567", usage.InputTokens)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 2500 {
		t.Fatalf("output = %v, want 2500", usage.OutputTokens)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 1000 {
		t.Fatalf("cached = %v, want 1000", usage.CachedInputTokens)
	}
	if usage.ReportedTotalTokens != nil {
		t.Fatalf("total = %v, want nil", usage.ReportedTotalTokens)
	}
	if usage.Provenance != "cli/usage_regex" {
		t.Fatalf("provenance = %q", usage.Provenance)
	}

	// The regex also sees the last 20 lines of stderr.
	errorsTail := Adapter{UsageRegex: `total:\s*(?P<total>[0-9][0-9,.]*)`}
	result = Result{ExitCode: 1, Stderr: []byte("total: 1\n" + strings.Repeat("noise\n", 22) + "total: 9,999\n")}
	errorsTail.Outcome(&result)
	if result.Usage == nil || result.Usage.ReportedTotalTokens == nil || *result.Usage.ReportedTotalTokens != 9999 {
		t.Fatalf("stderr tail usage = %#v", result.Usage)
	}

	// An optional group that did not participate leaves its counter nil.
	optional := Adapter{UsageRegex: `input:\s*(?P<input>[0-9]+)(?:\s+cached:\s*(?P<cached>[0-9]+))?`}
	result = Result{Stdout: []byte("input: 42\n")}
	optional.Outcome(&result)
	if result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 42 || result.Usage.CachedInputTokens != nil {
		t.Fatalf("optional cached usage = %#v", result.Usage)
	}

	// No match leaves Usage nil.
	missing := Adapter{UsageRegex: `total: (?P<total>[0-9]+)`}
	result = Result{Stdout: []byte("all done\n")}
	missing.Outcome(&result)
	if result.Usage != nil {
		t.Fatalf("usage = %#v, want nil", result.Usage)
	}
}

func TestOutcomeUsageTotalOnly(t *testing.T) {
	t.Parallel()
	adapter := Adapter{UsageRegex: `tokens used\s+(?P<total>[0-9][0-9., ]*)`}
	result := Result{ExitCode: 0, Stdout: []byte("the answer is written\n\ntokens used\n292.834\n")}
	adapter.Outcome(&result)
	if result.Usage == nil {
		t.Fatal("Outcome() did not fill Usage")
	}
	usage := *result.Usage
	if usage.ReportedTotalTokens == nil || *usage.ReportedTotalTokens != 292834 {
		t.Fatalf("total = %v, want 292834", usage.ReportedTotalTokens)
	}
	if usage.InputTokens != nil || usage.OutputTokens != nil || usage.CachedInputTokens != nil {
		t.Fatalf("usage = %#v, want only the reported total", usage)
	}
	if usage.Provenance != "cli/usage_regex" {
		t.Fatalf("provenance = %q", usage.Provenance)
	}
	if _, known := usage.TotalTokens(); known {
		t.Fatal("a total-only report invented input and output")
	}
}
