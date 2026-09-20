// Package judge asks a System One decision model typed noul, choice and score
// questions over a bounded state. Every failure surfaces as ErrUnavailable so
// callers keep today's deterministic rule.
package judge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var ErrUnavailable = errors.New("judge: unavailable")

const (
	ReasonJudgeOff              = "judge_off"
	ReasonKeyMissing            = "key_missing"
	ReasonTransportUndocumented = "transport_undocumented"
	ReasonTimeout               = "timeout"
	ReasonRateLimited           = "rate_limited"
	ReasonServerError           = "server_error"
	ReasonMalformedResponse     = "malformed_response"
	ReasonAnswerMismatch        = "answer_mismatch"
	ReasonStateTooLarge         = "state_too_large"
)

type Provider string

const (
	ProviderTypesafe   Provider = "typesafe"
	ProviderOpenRouter Provider = "openrouter"
	ProviderVercel     Provider = "vercel"
)

type QuestionType string

const (
	QuestionNoul   QuestionType = "noul"
	QuestionChoice QuestionType = "choice"
	QuestionScore  QuestionType = "score"
)

type Judge interface {
	Ask(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	Decision  string
	State     any
	Questions map[string]Question
}

type Question struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions,omitempty"`
	Criteria     any          `json:"criteria,omitempty"`
}

type Response struct {
	Model   string
	Answers map[string]Answer
	Usage   Usage
}

type Answer struct {
	Type          QuestionType       `json:"type"`
	Noul          float64            `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Options struct {
	Provider      Provider
	BaseURL       string
	Model         string
	Key           string
	Timeout       time.Duration
	MaxStateBytes int
	Client        *http.Client
}

type UnavailableError struct {
	Reason string
	Err    error
}

func (e *UnavailableError) Error() string {
	if e == nil {
		return ErrUnavailable.Error()
	}
	if e.Err != nil {
		return fmt.Sprintf("judge: unavailable (%s): %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("judge: unavailable (%s)", e.Reason)
}

func (e *UnavailableError) Unwrap() error {
	if e == nil {
		return ErrUnavailable
	}
	if e.Err != nil {
		return e.Err
	}
	return ErrUnavailable
}

func (e *UnavailableError) Is(target error) bool {
	return target == ErrUnavailable
}

func unavailable(reason string, err error) error {
	return &UnavailableError{Reason: reason, Err: err}
}
