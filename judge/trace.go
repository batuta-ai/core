package judge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const (
	KindIntent = "judge_intent"
	KindResult = "judge_result"
)

// Traced wraps a Judge and reports one intent record before and one result
// record after each Ask through Sink. Records never carry the state body,
// only its digest.
type Traced struct {
	Judge Judge
	Sink  func(kind string, record any) error
	Clock func() time.Time
}

var _ Judge = (*Traced)(nil)

// IntentRecord announces a decision before it is asked.
type IntentRecord struct {
	Decision     string   `json:"decision"`
	QuestionKeys []string `json:"question_keys"`
	StateDigest  string   `json:"state_digest"`
}

// ResultRecord reports the outcome of a decision. UnavailableReason is set
// only when the judge failed, in which case Model, Answers and Usage stay
// empty.
type ResultRecord struct {
	Decision          string            `json:"decision"`
	QuestionKeys      []string          `json:"question_keys"`
	StateDigest       string            `json:"state_digest"`
	Model             string            `json:"model,omitempty"`
	Answers           map[string]Answer `json:"answers,omitempty"`
	Usage             *Usage            `json:"usage,omitempty"`
	Latency           time.Duration     `json:"latency_ns,omitempty"`
	UnavailableReason string            `json:"unavailable_reason,omitempty"`
}

func (t *Traced) Ask(ctx context.Context, req Request) (Response, error) {
	digest, err := StateDigest(req.State)
	if err != nil {
		return Response{}, unavailable(ReasonMalformedResponse, err)
	}
	keys := sortedQuestionKeys(req.Questions)

	sinkErr := t.emit(KindIntent, IntentRecord{
		Decision:     req.Decision,
		QuestionKeys: keys,
		StateDigest:  digest,
	})

	start := t.now()
	resp, askErr := t.Judge.Ask(ctx, req)
	latency := t.now().Sub(start)

	result := ResultRecord{
		Decision:     req.Decision,
		QuestionKeys: keys,
		StateDigest:  digest,
		Latency:      latency,
	}
	if askErr == nil {
		result.Model = resp.Model
		result.Answers = resp.Answers
		usage := resp.Usage
		result.Usage = &usage
	} else {
		result.UnavailableReason = unavailableReason(askErr)
	}
	resultErr := t.emit(KindResult, result)

	return resp, errors.Join(sinkErr, resultErr, askErr)
}

func (t *Traced) now() time.Time {
	if t.Clock == nil {
		return time.Now()
	}
	return t.Clock()
}

func (t *Traced) emit(kind string, record any) error {
	if t.Sink == nil {
		return nil
	}
	return t.Sink(kind, record)
}

func sortedQuestionKeys(questions map[string]Question) []string {
	keys := make([]string, 0, len(questions))
	for key := range questions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func unavailableReason(err error) string {
	var unavailable *UnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Reason
	}
	return ""
}

// StateDigest canonicalizes state by re-encoding it as JSON with sorted keys
// and no insignificant whitespace, then returns "sha256:<hex>" over the result.
func StateDigest(state any) (string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
