package judge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ProviderAuto selects every configured provider whose key is present and
// falls through the chain when a call is unavailable.
const ProviderAuto Provider = "auto"

// ReasonAllUnavailable is returned when every provider in a Chain was skipped
// or failed with a fallback reason.
const ReasonAllUnavailable = "all_unavailable"

var defaultAutoProviders = []Provider{ProviderTypesafe, ProviderVercel, ProviderOpenRouter}

var fallbackReasons = map[string]struct{}{
	ReasonRateLimited:       {},
	ReasonServerError:       {},
	ReasonTimeout:           {},
	ReasonMalformedResponse: {},
	ReasonKeyMissing:        {},
	ReasonAnswerMismatch:    {},
}

// Named pairs a provider identity with the Judge that serves it.
type Named struct {
	Provider Provider
	Judge    Judge
}

// ChainAttempt records one provider that was skipped and why.
type ChainAttempt struct {
	Provider Provider `json:"provider"`
	Reason   string   `json:"reason"`
}

// ChainError wraps the per-provider reasons when a Chain is exhausted.
type ChainError struct {
	Attempts []ChainAttempt
}

func (e *ChainError) Error() string {
	if e == nil || len(e.Attempts) == 0 {
		return "all providers unavailable"
	}
	parts := make([]string, 0, len(e.Attempts))
	for _, attempt := range e.Attempts {
		parts = append(parts, fmt.Sprintf("%s: %s", attempt.Provider, attempt.Reason))
	}
	return strings.Join(parts, ", ")
}

// Chain asks Judges in order and returns the first Response.
type Chain struct {
	Judges []Named

	order    []Provider
	last     []ChainAttempt
	answered Provider
}

var _ Judge = (*Chain)(nil)

// LastAttempts returns the providers skipped on the last Ask (or at
// construction, those whose keys were missing).
func (c *Chain) LastAttempts() []ChainAttempt {
	if c == nil {
		return nil
	}
	return slices.Clone(c.last)
}

// LastProvider returns the provider that answered the last Ask.
func (c *Chain) LastProvider() Provider {
	if c == nil {
		return ""
	}
	return c.answered
}

func (c *Chain) Ask(ctx context.Context, req Request) (Response, error) {
	if c == nil {
		return Response{}, unavailable(ReasonAllUnavailable, &ChainError{})
	}

	byProvider := make(map[Provider]Judge, len(c.Judges))
	for _, named := range c.Judges {
		byProvider[named.Provider] = named.Judge
	}
	walk := c.order
	if len(walk) == 0 {
		walk = make([]Provider, 0, len(c.Judges))
		for _, named := range c.Judges {
			walk = append(walk, named.Provider)
		}
	}

	c.answered = ""
	attempts := make([]ChainAttempt, 0, len(walk))
	var lastMismatch Response
	for _, provider := range walk {
		j := byProvider[provider]
		if j == nil {
			attempts = append(attempts, ChainAttempt{Provider: provider, Reason: ReasonKeyMissing})
			continue
		}
		resp, err := j.Ask(ctx, req)
		if err == nil {
			c.answered = provider
			c.last = attempts
			return resp, nil
		}
		var unavailableErr *UnavailableError
		if !errors.As(err, &unavailableErr) {
			c.last = attempts
			return Response{}, err
		}
		if _, ok := fallbackReasons[unavailableErr.Reason]; !ok {
			c.last = attempts
			return Response{}, err
		}
		if unavailableErr.Reason == ReasonAnswerMismatch {
			lastMismatch = resp
		}
		attempts = append(attempts, ChainAttempt{Provider: provider, Reason: unavailableErr.Reason})
	}
	c.last = attempts
	return lastMismatch, unavailable(ReasonAllUnavailable, &ChainError{Attempts: slices.Clone(attempts)})
}
