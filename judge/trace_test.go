package judge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStateDigestCanonical(t *testing.T) {
	t.Parallel()

	padded := json.RawMessage(`{
		"b": [ "x",  "y"],
		"a": 1,
		"nested": { "z": true, "m": null }
	}`)
	compact := json.RawMessage(`{"a":1,"b":["x","y"],"nested":{"m":null,"z":true}}`)

	first, err := StateDigest(padded)
	if err != nil {
		t.Fatalf("StateDigest() error = %v", err)
	}
	second, err := StateDigest(compact)
	if err != nil {
		t.Fatalf("StateDigest() error = %v", err)
	}
	if first != second {
		t.Fatalf("digest changed across key order and whitespace: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("digest = %q, want sha256:<hex> prefix", first)
	}
	if len(first) != len("sha256:")+64 {
		t.Fatalf("digest = %q, want 64 hex chars after prefix", first)
	}

	other, err := StateDigest(json.RawMessage(`{"a":2,"b":["x","y"],"nested":{"m":null,"z":true}}`))
	if err != nil {
		t.Fatalf("StateDigest() error = %v", err)
	}
	if other == first {
		t.Fatalf("different states share digest %q", first)
	}
}

func TestStateDigestUnmarshalableState(t *testing.T) {
	t.Parallel()

	if _, err := StateDigest(make(chan int)); err == nil {
		t.Fatal("StateDigest(channel) error = nil, want error")
	}
}

type recordingSink struct {
	sequence *[]string
	kinds    []string
	records  []any
	err      error
}

func (s *recordingSink) sink(kind string, record any) error {
	*s.sequence = append(*s.sequence, kind)
	s.kinds = append(s.kinds, kind)
	s.records = append(s.records, record)
	return s.err
}

type fakeClock struct {
	current time.Time
	step    time.Duration
}

func (c *fakeClock) now() time.Time {
	current := c.current
	c.current = c.current.Add(c.step)
	return current
}

type fakeJudge struct {
	sequence *[]string
	resp     Response
	err      error
}

func (f *fakeJudge) Ask(ctx context.Context, req Request) (Response, error) {
	*f.sequence = append(*f.sequence, "ask")
	return f.resp, f.err
}

func TestTracedRecords(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		var sequence []string
		sink := &recordingSink{sequence: &sequence}
		clock := &fakeClock{current: time.Unix(0, 0).UTC(), step: 250 * time.Millisecond}
		traced := &Traced{
			Judge: &fakeJudge{
				sequence: &sequence,
				resp: Response{
					Model: "jev-1.13.0",
					Answers: map[string]Answer{
						"supported": {Type: QuestionNoul, Noul: 0.93},
					},
					Usage: Usage{InputTokens: 12, OutputTokens: 3},
				},
			},
			Sink:  sink.sink,
			Clock: clock.now,
		}

		state := map[string]any{"claim": "tests pass", "evidence": []any{"go test", "git status"}}
		resp, err := traced.Ask(context.Background(), Request{
			Decision: "claim_evidence",
			State:    state,
			Questions: map[string]Question{
				"zebra":     {Type: QuestionNoul, Instructions: "contradicted?"},
				"supported": {Type: QuestionNoul, Instructions: "supported?"},
			},
		})
		if err != nil {
			t.Fatalf("Ask() error = %v", err)
		}
		if resp.Model != "jev-1.13.0" {
			t.Fatalf("Ask() Model = %q", resp.Model)
		}
		if len(sink.records) != 2 {
			t.Fatalf("sink calls = %d, want 2", len(sink.records))
		}
		if !reflect.DeepEqual(sequence, []string{KindIntent, "ask", KindResult}) {
			t.Fatalf("sequence = %v, want intent before ask before result", sequence)
		}
		if sink.kinds[0] != "judge_intent" || sink.kinds[1] != "judge_result" {
			t.Fatalf("kinds = %v", sink.kinds)
		}

		intent, ok := sink.records[0].(IntentRecord)
		if !ok {
			t.Fatalf("intent record type = %T", sink.records[0])
		}
		if intent.Decision != "claim_evidence" {
			t.Fatalf("intent decision = %q", intent.Decision)
		}
		if !reflect.DeepEqual(intent.QuestionKeys, []string{"supported", "zebra"}) {
			t.Fatalf("intent question keys = %#v, want sorted", intent.QuestionKeys)
		}
		wantDigest, err := StateDigest(state)
		if err != nil {
			t.Fatalf("StateDigest() error = %v", err)
		}
		if intent.StateDigest != wantDigest {
			t.Fatalf("intent digest = %q, want %q", intent.StateDigest, wantDigest)
		}

		result, ok := sink.records[1].(ResultRecord)
		if !ok {
			t.Fatalf("result record type = %T", sink.records[1])
		}
		if result.Model != "jev-1.13.0" {
			t.Fatalf("result model = %q", result.Model)
		}
		if !reflect.DeepEqual(result.Answers, resp.Answers) {
			t.Fatalf("result answers = %#v", result.Answers)
		}
		wantUsage := Usage{InputTokens: 12, OutputTokens: 3}
		if result.Usage == nil || *result.Usage != wantUsage {
			t.Fatalf("result usage = %#v, want %v", result.Usage, wantUsage)
		}
		if result.Latency != 250*time.Millisecond {
			t.Fatalf("result latency = %v, want 250ms", result.Latency)
		}
		if result.UnavailableReason != "" {
			t.Fatalf("result unavailable reason = %q, want empty", result.UnavailableReason)
		}
		if result.Decision != intent.Decision || result.StateDigest != intent.StateDigest {
			t.Fatalf("result identity = %#v, want same as intent", result)
		}

		encoded, err := json.Marshal([]any{intent, result})
		if err != nil {
			t.Fatalf("marshal records: %v", err)
		}
		if strings.Contains(string(encoded), "tests pass") {
			t.Fatalf("records leak state body: %s", encoded)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		t.Parallel()

		var sequence []string
		sink := &recordingSink{sequence: &sequence}
		traced := &Traced{
			Judge: &fakeJudge{sequence: &sequence, err: &UnavailableError{Reason: ReasonRateLimited}},
			Sink:  sink.sink,
		}

		resp, err := traced.Ask(context.Background(), Request{
			Decision:  "claim_evidence",
			State:     "bounded evidence",
			Questions: map[string]Question{"supported": {Type: QuestionNoul}},
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Ask() error = %v, want ErrUnavailable", err)
		}
		if resp.Model != "" || len(resp.Answers) != 0 {
			t.Fatalf("Ask() response = %#v, want zero", resp)
		}
		if len(sink.records) != 2 {
			t.Fatalf("sink calls = %d, want 2", len(sink.records))
		}
		result, ok := sink.records[1].(ResultRecord)
		if !ok {
			t.Fatalf("result record type = %T", sink.records[1])
		}
		if result.UnavailableReason != ReasonRateLimited {
			t.Fatalf("unavailable reason = %q, want %q", result.UnavailableReason, ReasonRateLimited)
		}
		if result.Model != "" || result.Answers != nil || result.Usage != nil {
			t.Fatalf("result carries answer data on failure: %#v", result)
		}
	})
}

func TestTracedSinkError(t *testing.T) {
	t.Parallel()

	t.Run("with successful ask", func(t *testing.T) {
		t.Parallel()

		var sequence []string
		sinkErr := errors.New("sink boom")
		sink := &recordingSink{sequence: &sequence, err: sinkErr}
		traced := &Traced{
			Judge: &fakeJudge{
				sequence: &sequence,
				resp:     Response{Model: "jev-1.13.0", Answers: map[string]Answer{"supported": {Type: QuestionNoul, Noul: 0.93}}},
			},
			Sink: sink.sink,
		}

		resp, err := traced.Ask(context.Background(), Request{
			Decision:  "claim_evidence",
			State:     "bounded evidence",
			Questions: map[string]Question{"supported": {Type: QuestionNoul}},
		})
		if err == nil {
			t.Fatal("Ask() error = nil, want sink error")
		}
		if !strings.Contains(err.Error(), "sink boom") {
			t.Fatalf("Ask() error = %v, want sink error alongside", err)
		}
		if resp.Model != "jev-1.13.0" || len(resp.Answers) != 1 {
			t.Fatalf("Ask() response = %#v, want judge result unchanged", resp)
		}
		if len(sink.records) != 2 {
			t.Fatalf("sink calls = %d, want 2 despite errors", len(sink.records))
		}
	})

	t.Run("with unavailable ask", func(t *testing.T) {
		t.Parallel()

		var sequence []string
		sink := &recordingSink{sequence: &sequence, err: errors.New("sink boom")}
		traced := &Traced{
			Judge: &fakeJudge{sequence: &sequence, err: &UnavailableError{Reason: ReasonTimeout}},
			Sink:  sink.sink,
		}

		_, err := traced.Ask(context.Background(), Request{
			Decision:  "claim_evidence",
			State:     "bounded evidence",
			Questions: map[string]Question{"supported": {Type: QuestionNoul}},
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Ask() error = %v, want ErrUnavailable preserved", err)
		}
		if !strings.Contains(err.Error(), "sink boom") {
			t.Fatalf("Ask() error = %v, want sink error alongside", err)
		}
		if len(sink.records) != 2 {
			t.Fatalf("sink calls = %d, want 2 despite errors", len(sink.records))
		}
	})
}
