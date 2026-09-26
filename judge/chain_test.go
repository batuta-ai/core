package judge

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type staticJudge struct {
	resp Response
	err  error
}

func (s staticJudge) Ask(context.Context, Request) (Response, error) {
	return s.resp, s.err
}

type countingJudge struct {
	calls *int
	resp  Response
	err   error
}

func (c countingJudge) Ask(context.Context, Request) (Response, error) {
	*c.calls++
	return c.resp, c.err
}

func TestChainFallbackReasons(t *testing.T) {
	t.Parallel()

	success := Response{Model: "winner", Answers: map[string]Answer{"ok": {Type: QuestionNoul, Noul: 1}}}
	req := Request{State: "state", Questions: map[string]Question{"ok": {Type: QuestionNoul}}}

	for _, reason := range []string{ReasonRateLimited, ReasonServerError, ReasonTimeout, ReasonMalformedResponse, ReasonAnswerMismatch} {
		reason := reason
		t.Run("continues on "+reason, func(t *testing.T) {
			t.Parallel()
			var second int
			chain := &Chain{Judges: []Named{
				{Provider: ProviderTypesafe, Judge: staticJudge{err: &UnavailableError{Reason: reason}}},
				{Provider: ProviderVercel, Judge: countingJudge{calls: &second, resp: success}},
			}}
			resp, err := chain.Ask(context.Background(), req)
			if err != nil {
				t.Fatalf("Ask() error = %v", err)
			}
			if resp.Model != "winner" {
				t.Fatalf("Ask() Model = %q, want winner", resp.Model)
			}
			if second != 1 {
				t.Fatalf("second provider calls = %d, want 1", second)
			}
			if chain.LastProvider() != ProviderVercel {
				t.Fatalf("LastProvider() = %q, want vercel", chain.LastProvider())
			}
			if !reflect.DeepEqual(chain.LastAttempts(), []ChainAttempt{{Provider: ProviderTypesafe, Reason: reason}}) {
				t.Fatalf("LastAttempts() = %#v", chain.LastAttempts())
			}
		})
	}

	for _, reason := range []string{ReasonStateTooLarge} {
		reason := reason
		t.Run("stops on "+reason, func(t *testing.T) {
			t.Parallel()
			var second int
			chain := &Chain{Judges: []Named{
				{Provider: ProviderTypesafe, Judge: staticJudge{err: &UnavailableError{Reason: reason}}},
				{Provider: ProviderVercel, Judge: countingJudge{calls: &second, resp: success}},
			}}
			_, err := chain.Ask(context.Background(), req)
			requireUnavailable(t, err, reason)
			if second != 0 {
				t.Fatalf("second provider calls = %d, want 0", second)
			}
		})
	}

	t.Run("stops on non-unavailable", func(t *testing.T) {
		t.Parallel()
		var second int
		plain := errors.New("transport exploded")
		chain := &Chain{Judges: []Named{
			{Provider: ProviderTypesafe, Judge: staticJudge{err: plain}},
			{Provider: ProviderVercel, Judge: countingJudge{calls: &second, resp: success}},
		}}
		_, err := chain.Ask(context.Background(), req)
		if !errors.Is(err, plain) {
			t.Fatalf("Ask() error = %v, want %v", err, plain)
		}
		if second != 0 {
			t.Fatalf("second provider calls = %d, want 0", second)
		}
	})

	t.Run("stops on success", func(t *testing.T) {
		t.Parallel()
		var second int
		chain := &Chain{Judges: []Named{
			{Provider: ProviderTypesafe, Judge: staticJudge{resp: success}},
			{Provider: ProviderVercel, Judge: countingJudge{calls: &second, resp: Response{Model: "no"}}},
		}}
		resp, err := chain.Ask(context.Background(), req)
		if err != nil {
			t.Fatalf("Ask() error = %v", err)
		}
		if resp.Model != "winner" {
			t.Fatalf("Ask() Model = %q, want winner", resp.Model)
		}
		if second != 0 {
			t.Fatalf("second provider calls = %d, want 0", second)
		}
		if chain.LastProvider() != ProviderTypesafe {
			t.Fatalf("LastProvider() = %q, want typesafe", chain.LastProvider())
		}
		if len(chain.LastAttempts()) != 0 {
			t.Fatalf("LastAttempts() = %#v, want none", chain.LastAttempts())
		}
	})
}

func TestChainAllUnavailable(t *testing.T) {
	t.Parallel()

	chain := &Chain{Judges: []Named{
		{Provider: ProviderTypesafe, Judge: staticJudge{err: &UnavailableError{Reason: ReasonRateLimited}}},
		{Provider: ProviderVercel, Judge: staticJudge{err: &UnavailableError{Reason: ReasonTimeout}}},
		{Provider: ProviderOpenRouter, Judge: staticJudge{err: &UnavailableError{Reason: ReasonServerError}}},
	}}
	_, err := chain.Ask(context.Background(), Request{
		State:     "state",
		Questions: map[string]Question{"ok": {Type: QuestionNoul}},
	})
	requireUnavailable(t, err, ReasonAllUnavailable)
	var chainErr *ChainError
	if !errors.As(err, &chainErr) {
		t.Fatalf("Ask() error = %v, want ChainError via errors.As", err)
	}
	want := []ChainAttempt{
		{Provider: ProviderTypesafe, Reason: ReasonRateLimited},
		{Provider: ProviderVercel, Reason: ReasonTimeout},
		{Provider: ProviderOpenRouter, Reason: ReasonServerError},
	}
	if !reflect.DeepEqual(chainErr.Attempts, want) {
		t.Fatalf("ChainError.Attempts = %#v, want %#v", chainErr.Attempts, want)
	}
}

func TestChainReturnsPartialOnMismatch(t *testing.T) {
	t.Parallel()

	first := Response{Model: "first", Answers: map[string]Answer{"contract": {Type: QuestionNoul, Noul: 0.9}}}
	last := Response{Model: "last", Answers: map[string]Answer{"security": {Type: QuestionNoul, Noul: 0.8}}}
	chain := &Chain{Judges: []Named{
		{Provider: ProviderTypesafe, Judge: staticJudge{resp: first, err: &UnavailableError{Reason: ReasonAnswerMismatch}}},
		{Provider: ProviderVercel, Judge: staticJudge{err: &UnavailableError{Reason: ReasonTimeout}}},
		{Provider: ProviderOpenRouter, Judge: staticJudge{resp: last, err: &UnavailableError{Reason: ReasonAnswerMismatch}}},
	}}
	resp, err := chain.Ask(context.Background(), Request{})
	requireUnavailable(t, err, ReasonAllUnavailable)
	if !reflect.DeepEqual(resp, last) {
		t.Fatalf("Ask() response = %#v, want last mismatch %#v", resp, last)
	}
	var chainErr *ChainError
	if !errors.As(err, &chainErr) || !reflect.DeepEqual(chainErr.Attempts, []ChainAttempt{
		{Provider: ProviderTypesafe, Reason: ReasonAnswerMismatch},
		{Provider: ProviderVercel, Reason: ReasonTimeout},
		{Provider: ProviderOpenRouter, Reason: ReasonAnswerMismatch},
	}) {
		t.Fatalf("Ask() error = %v, want chain attempts", err)
	}

	chain = &Chain{Judges: []Named{
		{Provider: ProviderTypesafe, Judge: staticJudge{resp: first, err: &UnavailableError{Reason: ReasonAnswerMismatch}}},
		{Provider: ProviderVercel, Judge: staticJudge{err: &UnavailableError{Reason: ReasonTimeout}}},
	}}
	resp, err = chain.Ask(context.Background(), Request{})
	requireUnavailable(t, err, ReasonAllUnavailable)
	if !reflect.DeepEqual(resp, first) {
		t.Fatalf("Ask() response = %#v, want earlier mismatch %#v", resp, first)
	}
}
