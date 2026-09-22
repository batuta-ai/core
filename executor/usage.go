package executor

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// CacheSemantics says how a provider counts cached tokens against the
// input counters. Additive providers report cache reads and writes on top
// of InputTokens; subset providers report them as part of InputTokens.
type CacheSemantics string

const (
	CacheSemanticsAdditive CacheSemantics = "additive"
	CacheSemanticsSubset   CacheSemantics = "subset"
)

// Usage reports provider-supplied token counters. Nil counters are unknown;
// they are never estimated from byte counts. CachedInputTokens stays for
// compatibility and means cache read under subset semantics, so it is
// already inside InputTokens and must not be added to it; CacheReadTokens
// and CacheWriteTokens carry the cache counters of an additive provider.
// ReasoningTokens counts the reasoning part of the output.
// ReportedTotalTokens carries a total a CLI printed without separating
// input from output; it is never summed into TotalTokens. CostAmount with
// CostCurrency is copied from the provider, never computed and never part
// of TotalTokens.
type Usage struct {
	InputTokens         *int64         `json:"input_tokens,omitempty"`
	CachedInputTokens   *int64         `json:"cached_input_tokens,omitempty"`
	OutputTokens        *int64         `json:"output_tokens,omitempty"`
	CacheReadTokens     *int64         `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens    *int64         `json:"cache_write_tokens,omitempty"`
	ReasoningTokens     *int64         `json:"reasoning_tokens,omitempty"`
	ReportedTotalTokens *int64         `json:"reported_total_tokens,omitempty"`
	CostAmount          *float64       `json:"cost_amount,omitempty"`
	CostCurrency        string         `json:"cost_currency,omitempty"`
	CacheSemantics      CacheSemantics `json:"cache_semantics,omitempty"`
	Provenance          string         `json:"provenance"`
}

// TotalTokens returns the provider total when both input and output are
// known: input plus output plus the reported cache reads and writes under
// additive semantics, input plus output under subset semantics (the
// default, where CachedInputTokens is already inside input). Reported
// totals and cost never enter the sum.
func (u Usage) TotalTokens() (int64, bool) {
	if u.InputTokens == nil || u.OutputTokens == nil || *u.InputTokens < 0 || *u.OutputTokens < 0 {
		return 0, false
	}
	total := *u.InputTokens
	if *u.OutputTokens > math.MaxInt64-total {
		return 0, false
	}
	total += *u.OutputTokens
	if u.CacheSemantics == CacheSemanticsAdditive {
		for _, counter := range []*int64{u.CacheReadTokens, u.CacheWriteTokens} {
			if counter == nil {
				continue
			}
			if *counter < 0 || *counter > math.MaxInt64-total {
				return 0, false
			}
			total += *counter
		}
	}
	return total, true
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
