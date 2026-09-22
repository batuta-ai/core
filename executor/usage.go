package executor

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Usage reports provider-supplied token counters. Nil counters are unknown;
// they are never estimated from byte counts. CachedInputTokens is the cached
// subset of InputTokens and must not be added to it. ReportedTotalTokens
// carries a total a CLI printed without separating input from output; it is
// never summed into TotalTokens.
type Usage struct {
	InputTokens         *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens   *int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens        *int64 `json:"output_tokens,omitempty"`
	ReportedTotalTokens *int64 `json:"reported_total_tokens,omitempty"`
	Provenance          string `json:"provenance"`
}

// TotalTokens returns the provider total when both additive counters are
// known. Cached input is already included in input and is not counted twice.
func (u Usage) TotalTokens() (int64, bool) {
	if u.InputTokens == nil || u.OutputTokens == nil || *u.InputTokens < 0 || *u.OutputTokens < 0 {
		return 0, false
	}
	if *u.OutputTokens > math.MaxInt64-*u.InputTokens {
		return 0, false
	}
	return *u.InputTokens + *u.OutputTokens, true
}

// usageFromTail extracts the token counters a CLI printed in its output
// tail: every named group `input`, `output`, `cached` and `total` that
// participated in the match becomes the matching counter, with thousands
// separators removed. A group that did not match, or whose capture is not a
// usable count, leaves its counter nil; no counter means no usage.
func usageFromTail(pattern *regexp.Regexp, tail string) *Usage {
	match := pattern.FindStringSubmatch(tail)
	if match == nil {
		return nil
	}
	usage := &Usage{Provenance: "cli/usage_regex"}
	found := false
	for index, name := range pattern.SubexpNames() {
		if index == 0 || match[index] == "" {
			continue
		}
		count, ok := usageCount(match[index])
		if !ok {
			continue
		}
		counter := count
		switch name {
		case "input":
			usage.InputTokens = &counter
		case "output":
			usage.OutputTokens = &counter
		case "cached":
			usage.CachedInputTokens = &counter
		case "total":
			usage.ReportedTotalTokens = &counter
		default:
			continue
		}
		found = true
	}
	if !found {
		return nil
	}
	return usage
}

// usageCount parses a captured count, ignoring the thousands separators a
// CLI may print — comma, dot, thin space — and the plain space the example
// patterns capture.
func usageCount(value string) (int64, bool) {
	var digits strings.Builder
	for _, char := range value {
		switch char {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			digits.WriteRune(char)
		case ',', '.', ' ', '\u2009':
		default:
			return 0, false
		}
	}
	count, err := strconv.ParseInt(digits.String(), 10, 64)
	if err != nil {
		return 0, false
	}
	return count, true
}
