package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTypesafeBaseURL   = "https://api.typesafe.ai"
	defaultOpenRouterBaseURL = "https://openrouter.ai/api/alpha"
	defaultVercelBaseURL     = "https://ai-gateway.vercel.sh/typesafe"
	defaultTypesafeModel     = "jev-latest"
	defaultOpenRouterModel   = "typesafe/jev-1.13"
	defaultVercelModel       = "typesafe-ai/jev"
	typesafePath             = "/v1/systemone"
	openRouterPath           = "/decisions"
)

type HTTPJudge struct {
	provider      Provider
	baseURL       string
	path          string
	model         string
	key           string
	timeout       time.Duration
	maxStateBytes int
	client        *http.Client
}

var _ Judge = (*HTTPJudge)(nil)

func NewHTTPJudge(opts Options) *HTTPJudge {
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	j := &HTTPJudge{
		provider:      opts.Provider,
		baseURL:       strings.TrimSpace(opts.BaseURL),
		model:         strings.TrimSpace(opts.Model),
		key:           opts.Key,
		timeout:       opts.Timeout,
		maxStateBytes: opts.MaxStateBytes,
		client:        client,
	}
	switch j.provider {
	case ProviderOpenRouter:
		j.path = openRouterPath
		if j.baseURL == "" {
			j.baseURL = defaultOpenRouterBaseURL
		}
		if j.model == "" {
			j.model = defaultOpenRouterModel
		}
	case ProviderVercel:
		j.path = typesafePath
		if j.baseURL == "" {
			j.baseURL = defaultVercelBaseURL
		}
		if j.model == "" {
			j.model = defaultVercelModel
		}
	default:
		j.path = typesafePath
		if j.baseURL == "" {
			j.baseURL = defaultTypesafeBaseURL
		}
		if j.model == "" {
			j.model = defaultTypesafeModel
		}
	}
	return j
}

func (j *HTTPJudge) Ask(ctx context.Context, req Request) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if j.key == "" {
		return Response{}, unavailable(ReasonKeyMissing, nil)
	}

	stateJSON, err := json.Marshal(req.State)
	if err != nil {
		return Response{}, unavailable(ReasonMalformedResponse, err)
	}
	if j.maxStateBytes > 0 && len(stateJSON) > j.maxStateBytes {
		return Response{}, unavailable(ReasonStateTooLarge, nil)
	}

	payload, err := json.Marshal(struct {
		State     json.RawMessage     `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{
		State:     stateJSON,
		Model:     j.model,
		Questions: req.Questions,
	})
	if err != nil {
		return Response{}, unavailable(ReasonMalformedResponse, err)
	}

	if j.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, j.timeout)
		defer cancel()
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, j.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return Response{}, unavailable(ReasonServerError, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+j.key)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := j.client.Do(httpReq)
	if err != nil {
		if isTimeout(err) {
			return Response{}, unavailable(ReasonTimeout, err)
		}
		return Response{}, unavailable(ReasonServerError, err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		if isTimeout(err) {
			return Response{}, unavailable(ReasonTimeout, err)
		}
		return Response{}, unavailable(ReasonMalformedResponse, err)
	}

	if httpResp.StatusCode == http.StatusTooManyRequests {
		return Response{}, unavailable(ReasonRateLimited, fmt.Errorf("http %d", httpResp.StatusCode))
	}
	if httpResp.StatusCode >= 500 {
		return Response{}, unavailable(ReasonServerError, fmt.Errorf("http %d", httpResp.StatusCode))
	}

	if reason, ok := openRouterErrorReason(body); ok {
		return Response{}, unavailable(reason, nil)
	}
	if j.provider == ProviderVercel {
		if reason, ok := gatewayErrorReason(body, httpResp.StatusCode); ok {
			return Response{}, unavailable(reason, nil)
		}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return Response{}, unavailable(ReasonServerError, fmt.Errorf("http %d", httpResp.StatusCode))
	}

	var parsed struct {
		Model   string            `json:"model"`
		Answers map[string]Answer `json:"answers"`
		Usage   Usage             `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, unavailable(ReasonMalformedResponse, err)
	}
	if parsed.Answers == nil {
		return Response{}, unavailable(ReasonMalformedResponse, errors.New("missing answers"))
	}
	if err := validateAnswers(req.Questions, parsed.Answers); err != nil {
		return Response{Model: parsed.Model, Answers: matchingAnswers(req.Questions, parsed.Answers), Usage: parsed.Usage}, err
	}
	return Response{Model: parsed.Model, Answers: parsed.Answers, Usage: parsed.Usage}, nil
}

func (j *HTTPJudge) endpoint() string {
	return strings.TrimRight(j.baseURL, "/") + j.path
}

func validateAnswers(questions map[string]Question, answers map[string]Answer) error {
	if len(answers) != len(questions) {
		return unavailable(ReasonAnswerMismatch, fmt.Errorf("got %d answers, want %d", len(answers), len(questions)))
	}
	for key, question := range questions {
		answer, ok := answers[key]
		if !ok {
			return unavailable(ReasonAnswerMismatch, fmt.Errorf("missing %s", key))
		}
		if answer.Type != question.Type {
			return unavailable(ReasonAnswerMismatch, fmt.Errorf("%s type %s, want %s", key, answer.Type, question.Type))
		}
		if question.Type == QuestionChoice {
			options, ok := choiceOptions(question.Criteria)
			if !ok {
				return unavailable(ReasonAnswerMismatch, fmt.Errorf("%s criteria", key))
			}
			if _, exists := options[answer.Choice]; !exists {
				return unavailable(ReasonAnswerMismatch, fmt.Errorf("%s choice %q", key, answer.Choice))
			}
		}
	}
	return nil
}

func matchingAnswers(questions map[string]Question, answers map[string]Answer) map[string]Answer {
	matched := make(map[string]Answer)
	for key, answer := range answers {
		question, ok := questions[key]
		if !ok || answer.Type != question.Type {
			continue
		}
		if question.Type == QuestionChoice {
			options, ok := choiceOptions(question.Criteria)
			if !ok {
				continue
			}
			if _, ok := options[answer.Choice]; !ok {
				continue
			}
		}
		matched[key] = answer
	}
	return matched
}

func choiceOptions(criteria any) (map[string]struct{}, bool) {
	if criteria == nil {
		return nil, false
	}
	encoded, err := json.Marshal(criteria)
	if err != nil {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, false
	}
	options := make(map[string]struct{}, len(object))
	for key := range object {
		options[key] = struct{}{}
	}
	return options, true
}

func openRouterErrorReason(body []byte) (string, bool) {
	var envelope struct {
		Error *struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error == nil {
		return "", false
	}
	code := parseErrorCode(envelope.Error.Code)
	if code == strconv.Itoa(http.StatusTooManyRequests) {
		return ReasonRateLimited, true
	}
	return ReasonServerError, true
}

func gatewayErrorReason(body []byte, status int) (string, bool) {
	var envelope struct {
		Message   string `json:"message"`
		ErrorType string `json:"error_type"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.ErrorType == "" {
		return "", false
	}
	switch {
	case status == http.StatusTooManyRequests:
		return ReasonRateLimited, true
	case status >= 500:
		return ReasonServerError, true
	case status >= 400:
		return "gateway_" + envelope.ErrorType, true
	}
	return "", false
}

func parseErrorCode(raw json.RawMessage) string {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return strconv.Itoa(n)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
