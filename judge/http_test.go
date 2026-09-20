package judge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		t.Cleanup(srv.Close)
		_, err := NewHTTPJudge(Options{
			Provider: ProviderVercel,
			BaseURL:  srv.URL,
			Key:      "test-key",
		}).Ask(context.Background(), probeRequest())
		if called {
			t.Fatal("vercel issued an HTTP request")
		}
		requireUnavailable(t, err, "transport_undocumented")
	})
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
