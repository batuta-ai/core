package judge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHTTPJudgeAnswers(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model": "jev-1.13.0",
			"answers": {
				"supported": {"type": "noul", "noul": 0.93},
				"department": {"type": "choice", "choice": "k", "confidence": 0.78, "probabilities": {"k": 0.78, "other": 0.22}},
				"urgency": {"type": "score", "score": 1.0, "confidence": 1.0, "probabilities": {"low": 0.0, "high": 1.0}}
			},
			"usage": {"input_tokens": 12, "output_tokens": 3}
		}`)
	}))
	t.Cleanup(srv.Close)

	resp, err := NewHTTPJudge(Options{
		Provider:      ProviderTypesafe,
		BaseURL:       srv.URL,
		Model:         "jev-latest",
		Key:           "test-key",
		MaxStateBytes: 4096,
	}).Ask(context.Background(), Request{
		Decision: "probe",
		State:    "bounded evidence",
		Questions: map[string]Question{
			"supported":  {Type: QuestionNoul, Instructions: "does the evidence support the claim?"},
			"department": {Type: QuestionChoice, Instructions: "which bucket?", Criteria: map[string]string{"k": "keep", "other": "unclear"}},
			"urgency":    {Type: QuestionScore, Instructions: "how urgent?", Criteria: []string{"low", "high"}},
		},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if resp.Model != "jev-1.13.0" {
		t.Fatalf("Model = %q, want jev-1.13.0", resp.Model)
	}

	noul, ok := resp.Answers["supported"]
	if !ok || noul.Type != QuestionNoul || noul.Noul != 0.93 {
		t.Fatalf("noul answer = %#v", noul)
	}

	choice, ok := resp.Answers["department"]
	if !ok || choice.Type != QuestionChoice || choice.Choice != "k" || choice.Confidence != 0.78 {
		t.Fatalf("choice answer = %#v", choice)
	}
	if !reflect.DeepEqual(choice.Probabilities, map[string]float64{"k": 0.78, "other": 0.22}) {
		t.Fatalf("choice probabilities = %#v", choice.Probabilities)
	}

	score, ok := resp.Answers["urgency"]
	if !ok || score.Type != QuestionScore || score.Score != 1 || score.Confidence != 1 {
		t.Fatalf("score answer = %#v", score)
	}
	if !reflect.DeepEqual(score.Probabilities, map[string]float64{"low": 0, "high": 1}) {
		t.Fatalf("score probabilities = %#v", score.Probabilities)
	}
}

type staticHTTPTransport struct {
	body string
}

func (transport staticHTTPTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(transport.body))}, nil
}

func TestHTTPJudgePartialAnswersOnMismatch(t *testing.T) {
	t.Parallel()

	questions := map[string]Question{
		"valid":   {Type: QuestionNoul},
		"choice":  {Type: QuestionChoice, Criteria: map[string]string{"known": "known option"}},
		"missing": {Type: QuestionScore},
	}
	for _, tc := range []struct {
		name    string
		answers string
		want    map[string]Answer
	}{
		{
			name:    "missing answer and unknown choice",
			answers: `{"valid":{"type":"noul","noul":0.9},"choice":{"type":"choice","choice":"unknown"}}`,
			want:    map[string]Answer{"valid": {Type: QuestionNoul, Noul: 0.9}},
		},
		{
			name:    "wrong type and extra key",
			answers: `{"valid":{"type":"score","score":1},"choice":{"type":"choice","choice":"known"},"missing":{"type":"score","score":1},"extra":{"type":"noul","noul":1}}`,
			want: map[string]Answer{
				"choice":  {Type: QuestionChoice, Choice: "known"},
				"missing": {Type: QuestionScore, Score: 1},
			},
		},
		{
			name:    "no usable answer",
			answers: `{"valid":{"type":"score","score":1}}`,
			want:    map[string]Answer{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j := NewHTTPJudge(Options{
				Key: "test-key",
				Client: &http.Client{Transport: staticHTTPTransport{
					body: `{"model":"partial","answers":` + tc.answers + `,"usage":{"input_tokens":7}}`,
				}},
			})
			resp, err := j.Ask(context.Background(), Request{Questions: questions})
			requireUnavailable(t, err, ReasonAnswerMismatch)
			if resp.Model != "partial" || resp.Usage.InputTokens != 7 || !reflect.DeepEqual(resp.Answers, tc.want) {
				t.Fatalf("Ask() response = %#v, want model, usage and answers %#v", resp, tc.want)
			}
		})
	}
}

func TestResponseJSON(t *testing.T) {
	t.Parallel()

	response := Response{
		Model: "jev-1.13.0",
		Answers: map[string]Answer{
			"supported": {Type: QuestionNoul, Noul: 0.93},
			"zero":      {Type: QuestionNoul},
			"choice":    {Type: QuestionChoice, Choice: "k", Confidence: 0.78, Probabilities: map[string]float64{"k": 0.78, "other": 0.22}},
			"score":     {Type: QuestionScore, Score: 1},
		},
		Usage: Usage{InputTokens: 12, OutputTokens: 3},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := generic["model"]; !ok {
		t.Fatalf("missing key model: %s", encoded)
	}
	if _, ok := generic["answers"]; !ok {
		t.Fatalf("missing key answers: %s", encoded)
	}
	usage, ok := generic["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v, want an object", generic["usage"])
	}
	if _, ok := usage["input_tokens"]; !ok {
		t.Fatalf("missing key input_tokens: %s", encoded)
	}
	if _, ok := usage["output_tokens"]; !ok {
		t.Fatalf("missing key output_tokens: %s", encoded)
	}

	answers, ok := generic["answers"].(map[string]any)
	if !ok {
		t.Fatalf("answers = %#v, want an object", generic["answers"])
	}
	cases := []struct {
		name   string
		want   map[string]any
		absent []string
	}{
		{"supported", map[string]any{"type": "noul", "noul": 0.93}, []string{"choice", "score", "confidence", "probabilities"}},
		{"zero", map[string]any{"type": "noul", "noul": float64(0)}, []string{"choice", "score", "confidence", "probabilities"}},
		{"choice", map[string]any{"type": "choice", "choice": "k", "confidence": 0.78}, []string{"noul", "score"}},
		{"score", map[string]any{"type": "score", "score": float64(1)}, []string{"noul", "choice", "confidence"}},
	}
	for _, tc := range cases {
		answer, ok := answers[tc.name].(map[string]any)
		if !ok {
			t.Fatalf("answer %s = %#v, want an object", tc.name, answers[tc.name])
		}
		for key, want := range tc.want {
			if answer[key] != want {
				t.Fatalf("answer %s[%q] = %#v, want %#v: %s", tc.name, key, answer[key], want, encoded)
			}
		}
		for _, key := range tc.absent {
			if _, present := answer[key]; present {
				t.Fatalf("answer %s carries optional %q: %s", tc.name, key, encoded)
			}
		}
	}

	probabilities := answers["choice"].(map[string]any)["probabilities"].(map[string]any)
	if len(probabilities) != 2 || probabilities["k"] != 0.78 || probabilities["other"] != 0.22 {
		t.Fatalf("probabilities = %#v", probabilities)
	}
}

func TestHTTPJudgeRequestShape(t *testing.T) {
	t.Parallel()

	var (
		method string
		path   string
		auth   string
		body   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`)
	}))
	t.Cleanup(srv.Close)

	state := map[string]any{"claim": "done", "files": []any{"judge/http.go"}}
	_, err := NewHTTPJudge(Options{
		Provider:      ProviderTypesafe,
		BaseURL:       srv.URL,
		Model:         "jev-latest",
		Key:           "test-key",
		MaxStateBytes: 4096,
	}).Ask(context.Background(), Request{
		Decision: "claim_evidence",
		State:    state,
		Questions: map[string]Question{
			"q": {
				Type:         QuestionNoul,
				Instructions: "does the tree support the claim?",
				Criteria:     map[string]string{"true": "supported", "false": "unsupported"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("method = %q, want POST", method)
	}
	if path != "/v1/systemone" {
		t.Fatalf("path = %q, want /v1/systemone", path)
	}
	if auth != "Bearer test-key" {
		t.Fatalf("Authorization = %q", auth)
	}
	if len(body) != 3 {
		t.Fatalf("body keys = %#v, want state, model, questions", body)
	}
	if body["model"] != "jev-latest" {
		t.Fatalf("model = %#v", body["model"])
	}
	if !reflect.DeepEqual(body["state"], state) {
		t.Fatalf("state = %#v, want %#v", body["state"], state)
	}
	questions, _ := body["questions"].(map[string]any)
	question, _ := questions["q"].(map[string]any)
	if question["type"] != "noul" || question["instructions"] != "does the tree support the claim?" {
		t.Fatalf("question = %#v", question)
	}
	if !reflect.DeepEqual(question["criteria"], map[string]any{"true": "supported", "false": "unsupported"}) {
		t.Fatalf("criteria = %#v", question["criteria"])
	}
}

func TestHTTPJudgeUnavailable(t *testing.T) {
	t.Parallel()

	t.Run("rate_limited", func(t *testing.T) {
		t.Parallel()
		j := judgeAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}), Options{})
		_, err := j.Ask(context.Background(), probeRequest())
		requireUnavailable(t, err, "rate_limited")
	})

	t.Run("server_error", func(t *testing.T) {
		t.Parallel()
		j := judgeAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}), Options{})
		_, err := j.Ask(context.Background(), probeRequest())
		requireUnavailable(t, err, "server_error")
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		unblock := make(chan struct{})
		j := judgeAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-unblock
		}), Options{Timeout: 30 * time.Millisecond})
		t.Cleanup(func() { close(unblock) })
		_, err := j.Ask(context.Background(), probeRequest())
		requireUnavailable(t, err, "timeout")
	})

	t.Run("malformed_response", func(t *testing.T) {
		t.Parallel()
		j := judgeAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"model":`)
		}), Options{})
		_, err := j.Ask(context.Background(), probeRequest())
		requireUnavailable(t, err, "malformed_response")
	})

	t.Run("answer_mismatch", func(t *testing.T) {
		t.Parallel()
		j := judgeAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"other":{"type":"noul","noul":0.1}}}`)
		}), Options{})
		_, err := j.Ask(context.Background(), probeRequest())
		requireUnavailable(t, err, "answer_mismatch")
	})
}

func TestHTTPJudgeStateBudget(t *testing.T) {
	t.Parallel()

	called := false
	j := judgeAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}), Options{MaxStateBytes: 5})
	_, err := j.Ask(context.Background(), Request{
		State:     "abcd",
		Questions: map[string]Question{"q": {Type: QuestionNoul, Instructions: "x"}},
	})
	if called {
		t.Fatal("request was sent for an over-budget state")
	}
	requireUnavailable(t, err, "state_too_large")
}

func TestProviders(t *testing.T) {
	t.Parallel()

	t.Run("openrouter", func(t *testing.T) {
		t.Parallel()
		var (
			path  string
			model string
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			model = body.Model
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Too Many Requests"}}`)
		}))
		t.Cleanup(srv.Close)

		_, err := NewHTTPJudge(Options{
			Provider:      ProviderOpenRouter,
			BaseURL:       srv.URL,
			Key:           "test-key",
			MaxStateBytes: 4096,
		}).Ask(context.Background(), probeRequest())
		if path != "/decisions" {
			t.Fatalf("path = %q, want /decisions", path)
		}
		if model != "typesafe/jev-1.13" {
			t.Fatalf("model = %q, want typesafe/jev-1.13", model)
		}
		requireUnavailable(t, err, "rate_limited")
	})

	t.Run("vercel", func(t *testing.T) {
		t.Parallel()
		j := NewHTTPJudge(Options{Provider: ProviderVercel})
		if j.baseURL != defaultVercelBaseURL || j.path != typesafePath || j.model != defaultVercelModel {
			t.Fatalf("defaults = %q %q %q", j.baseURL, j.path, j.model)
		}

		var (
			path string
			auth string
			body struct {
				Model string `json:"model"`
			}
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			auth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"model": "typesafe-ai/jev",
				"answers": {"q": {"type": "noul", "noul": 0.9}},
				"usage": {"input_tokens": 7, "output_tokens": 1},
				"provider_metadata": {"gateway": {"cost": 0.00013, "generationId": "gw_1"}}
			}`)
		}))
		t.Cleanup(srv.Close)

		resp, err := NewHTTPJudge(Options{
			Provider:      ProviderVercel,
			BaseURL:       srv.URL,
			Key:           "test-key",
			MaxStateBytes: 4096,
		}).Ask(context.Background(), probeRequest())
		if err != nil {
			t.Fatalf("Ask() error = %v", err)
		}
		if path != "/v1/systemone" {
			t.Fatalf("path = %q, want /v1/systemone", path)
		}
		if auth != "Bearer test-key" {
			t.Fatalf("Authorization = %q", auth)
		}
		if body.Model != "typesafe-ai/jev" {
			t.Fatalf("model = %q, want typesafe-ai/jev", body.Model)
		}
		if resp.Model != "typesafe-ai/jev" {
			t.Fatalf("response model = %q, want typesafe-ai/jev", resp.Model)
		}
		answer, ok := resp.Answers["q"]
		if !ok || answer.Type != QuestionNoul || answer.Noul != 0.9 {
			t.Fatalf("noul answer = %#v", answer)
		}
		if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 1 {
			t.Fatalf("usage = %#v", resp.Usage)
		}
	})
}

func TestVercelErrorEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"gateway_reason", http.StatusBadRequest, `{"message":"state exceeds the context limit","error_type":"invalid_request"}`, "gateway_invalid_request"},
		{"rate_limited", http.StatusTooManyRequests, `{"message":"throttled","error_type":"rate_limit_exceeded"}`, "rate_limited"},
		{"server_error", http.StatusBadGateway, `{"message":"upstream failed","error_type":"upstream_error"}`, "server_error"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j := vercelJudge(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			_, err := j.Ask(context.Background(), probeRequest())
			requireUnavailable(t, err, tc.reason)
		})
	}
}

func judgeAgainst(t *testing.T, handler http.Handler, opts Options) *HTTPJudge {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	opts.Provider = ProviderTypesafe
	opts.BaseURL = srv.URL
	opts.Key = "test-key"
	if opts.MaxStateBytes == 0 {
		opts.MaxStateBytes = 4096
	}
	return NewHTTPJudge(opts)
}

func vercelJudge(t *testing.T, handler http.Handler) *HTTPJudge {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewHTTPJudge(Options{
		Provider:      ProviderVercel,
		BaseURL:       srv.URL,
		Key:           "test-key",
		MaxStateBytes: 4096,
	})
}

func probeRequest() Request {
	return Request{
		State:     "probe",
		Questions: map[string]Question{"q": {Type: QuestionNoul, Instructions: "yes?"}},
	}
}

func requireUnavailable(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Ask() error = nil, want ErrUnavailable (%s)", reason)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ask() error = %v, want ErrUnavailable", err)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != reason {
		t.Fatalf("Ask() error = %v, want reason %s", err, reason)
	}
}
