package executor

import "math"

// Usage reports provider-supplied token counters. Nil counters are unknown;
// they are never estimated from byte counts. CachedInputTokens is the cached
// subset of InputTokens and must not be added to it.
type Usage struct {
	InputTokens       *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens *int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens      *int64 `json:"output_tokens,omitempty"`
	Provenance        string `json:"provenance"`
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
