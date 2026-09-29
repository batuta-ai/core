package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/batuta-ai/core/classify"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

var benchV3Questions = []string{"contract", "security"}

type benchV3Packet struct {
	Found bool `json:"found"`
	Size  int  `json:"size"`
}

type benchV3Answer struct {
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Status        string             `json:"status"`
}

type benchV3Record struct {
	PlanSlug    string                   `json:"plan_slug"`
	Split       string                   `json:"split"`
	Task        string                   `json:"task"`
	PlanLane    string                   `json:"plan_lane"`
	CodeLane    string                   `json:"code_lane"`
	JudgeLane   string                   `json:"judge_lane"`
	Scope       classify.ScopeFeatures   `json:"scope"`
	OpenMarker  bool                     `json:"open_marker"`
	Packets     map[string]benchV3Packet `json:"packets"`
	Answers     map[string]benchV3Answer `json:"answers"`
	Outcome     string                   `json:"outcome"`
	InputTokens *int                     `json:"input_tokens,omitempty"`
	Called      bool                     `json:"called"`
	Unavailable string                   `json:"unavailable,omitempty"`
	Reason      string                   `json:"reason,omitempty"`
}

func (r benchV3Record) text() string {
	line := fmt.Sprintf("%s %s split=%s plan=%s code=%s judge=%s scope=files:%d,dirs:%d,test_only:%t,docs_only:%t open_marker=%t",
		r.PlanSlug, r.Task, r.Split, r.PlanLane, r.CodeLane, r.JudgeLane,
		r.Scope.Files, r.Scope.Directories, r.Scope.TestOnly, r.Scope.DocsOnly, r.OpenMarker)
	for _, key := range benchV3Questions {
		packet := r.Packets[key]
		answer := r.Answers[key]
		line += fmt.Sprintf(" %s=packet:%t,size:%d,status:%s", key, packet.Found, packet.Size, answer.Status)
		if answer.Choice != "" {
			line += fmt.Sprintf(",choice:%s,confidence:%.2f,probabilities:%v", answer.Choice, answer.Confidence, answer.Probabilities)
		}
	}
	line += fmt.Sprintf(" called=%t", r.Called)
	if r.InputTokens != nil {
		line += fmt.Sprintf(" input_tokens=%d", *r.InputTokens)
	}
	if r.Unavailable != "" {
		line += " unavailable=" + r.Unavailable
	}
	if r.Reason != "" {
		line += " reason=" + r.Reason
	}
	return line + " outcome=" + r.Outcome
}

func loadBenchV3Rule(path string) (classify.RuleV3, error) {
	if path == "" {
		return classify.DefaultRuleV3, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return classify.RuleV3{}, fmt.Errorf("judge classify bench: rule: %w", err)
	}
	var rule classify.RuleV3
	if err := json.Unmarshal(payload, &rule); err != nil {
		return rule, fmt.Errorf("judge classify bench: rule: %w", err)
	}
	if err := rule.Validate(); err != nil {
		return rule, fmt.Errorf("judge classify bench: rule: %w", err)
	}
	return rule, nil
}

func benchV3PlanRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for dir := filepath.Dir(absolute); ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == ".batuta" {
			return filepath.Dir(dir), nil
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", fmt.Errorf("judge classify bench: plan %s is outside .batuta", path)
		}
	}
}

func classifyBenchTasksV3(ctx context.Context, stdout io.Writer, j judge.Judge, buildReason string, plans []routing.Plan, paths []string, deliveries []benchDelivery, rule classify.RuleV3, split string, asJSON bool) error {
	encoder := json.NewEncoder(stdout)
	counts := newBenchV3Counts(rule, split)
	for i, plan := range plans {
		planSplit := classify.SplitV3(plan.Slug)
		if split != "" && split != planSplit {
			continue
		}
		root, err := benchV3PlanRoot(paths[i])
		if err != nil {
			return err
		}
		for _, task := range plan.Tasks {
			record := benchV3RecordFor(ctx, j, buildReason, plan, task, root, planSplit, deliveries, rule)
			counts.add(record)
			if asJSON {
				if err := encoder.Encode(record); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(stdout, record.text()); err != nil {
				return err
			}
		}
	}
	if asJSON {
		return encoder.Encode(map[string]benchV3Summary{"summary": counts.summary()})
	}
	return counts.printSummary(stdout)
}

func benchV3RecordFor(ctx context.Context, j judge.Judge, buildReason string, plan routing.Plan, task routing.PlanTask, root, split string, deliveries []benchDelivery, rule classify.RuleV3) benchV3Record {
	contextText := plan.ContextFor(task.Number)
	features := classify.Features(task)
	codeLane := classify.CodeLaneV3(task, contextText, features, rule)
	record := benchV3Record{
		PlanSlug: plan.Slug, Split: split, Task: task.ID, PlanLane: string(task.Complexity),
		CodeLane: string(codeLane), JudgeLane: string(codeLane), Scope: features,
		OpenMarker: classify.OpenMarker(task, contextText), Outcome: benchOutcomeUnknown,
		Packets: make(map[string]benchV3Packet), Answers: make(map[string]benchV3Answer),
	}
	if delivery := benchDeliveryFor(deliveries, plan.Slug, task.ID); delivery != nil {
		record.Outcome = benchOutcome(delivery.records, task.ID)
	}
	request, hasPacket := classify.BuildRequestV3(root, task, contextText)
	for _, key := range benchV3Questions {
		_, found := request.Questions[key]
		size := 0
		if found {
			size = benchV3PacketSize(request.State, key)
		}
		record.Packets[key] = benchV3Packet{Found: found, Size: size}
		// A question without a packet is never asked: it changes nothing, like an
		// insufficient answer, but it is not an answer and is not counted as one.
		status := "not_asked"
		if found {
			status = "unavailable"
		}
		record.Answers[key] = benchV3Answer{Status: status}
	}
	if !hasPacket {
		return record
	}
	if j == nil {
		record.Unavailable = buildReason
		return record
	}
	record.Called = true
	response, err := j.Ask(ctx, request)
	if response.Usage.InputTokens != 0 {
		tokens := response.Usage.InputTokens
		record.InputTokens = &tokens
	}
	if err != nil && (!benchV2AnswerMismatch(err) || !benchV3HasUsableAnswer(response.Answers, request.Questions)) {
		record.Unavailable = judgeReplayReason(err)
		return record
	}
	if err != nil {
		record.Reason = judge.ReasonAnswerMismatch
	}
	asked := make(map[string]bool)
	for key := range request.Questions {
		asked[key] = true
	}
	decision := classify.DecideV3(codeLane, asked, response.Answers, rule.Threshold)
	record.JudgeLane = string(decision.Complexity)
	for _, key := range benchV3Questions {
		if !asked[key] {
			continue
		}
		answer := response.Answers[key]
		if decision.Status[key] == "unavailable" {
			record.Answers[key] = benchV3Answer{Status: "unavailable"}
			continue
		}
		record.Answers[key] = benchV3Answer{
			Choice: answer.Choice, Confidence: answer.Confidence,
			Probabilities: answer.Probabilities, Status: decision.Status[key],
		}
	}
	return record
}

func benchV3PacketSize(state any, key string) int {
	// BuildRequestV3 keeps its wire state private to classify; measure the
	// serialized packet so the bench records precisely what the judge sees.
	payload, err := json.Marshal(state)
	if err != nil {
		return 0
	}
	var packets struct {
		Contract []string `json:"contract_packet"`
		Security []string `json:"security_packet"`
	}
	if json.Unmarshal(payload, &packets) != nil {
		return 0
	}
	if key == "contract" {
		return len(packets.Contract)
	}
	return len(packets.Security)
}

func benchV3HasUsableAnswer(answers map[string]judge.Answer, questions map[string]judge.Question) bool {
	for key := range questions {
		answer := answers[key]
		if answer.Type != judge.QuestionChoice {
			continue
		}
		for option := range questions[key].Criteria.(map[string]string) {
			if answer.Choice == option {
				return true
			}
		}
	}
	return false
}

type benchV3QuestionCounts struct {
	Packets        int `json:"packets"`
	Calls          int `json:"calls"`
	Firm           int `json:"firm"`
	Insufficient   int `json:"insufficient"`
	BelowThreshold int `json:"below_threshold"`
	Unavailable    int `json:"unavailable"`
	NotAsked       int `json:"not_asked"`
}

type benchV3LaneSummary struct {
	Economy        benchV2Measure        `json:"economy"`
	Safety         benchV2Measure        `json:"safety"`
	Discrimination benchV2Discrimination `json:"discrimination"`
	Balance        benchV2Measure        `json:"balance"`
	Agreement      benchV2Measure        `json:"agreement"`
	Pass           bool                  `json:"pass"`
}

type benchV3Rule struct {
	FHigh     int     `json:"f_high"`
	FLow      int     `json:"f_low"`
	DocsLow   bool    `json:"docs_low"`
	Threshold float64 `json:"threshold"`
}

type benchV3Summary struct {
	Split        string                           `json:"split,omitempty"`
	Rule         benchV3Rule                      `json:"rule"`
	Tasks        int                              `json:"tasks"`
	Sufficient   int                              `json:"sufficient"`
	Insufficient int                              `json:"insufficient"`
	Unknown      int                              `json:"unknown"`
	OpenMarkers  int                              `json:"open_markers"`
	InputTokens  int                              `json:"input_tokens"`
	Questions    map[string]benchV3QuestionCounts `json:"questions"`
	Code         benchV3LaneSummary               `json:"code"`
	Judge        benchV3LaneSummary               `json:"judge"`
	BalanceDelta float64                          `json:"balance_delta"`
}

type benchV3LaneCounts struct {
	economy, safety, agreement, measured int
	sufficient, insufficient             int
	distribution                         map[string]int
}

type benchV3Counts struct {
	data  benchV3Summary
	code  benchV3LaneCounts
	judge benchV3LaneCounts
}

func newBenchV3Counts(rule classify.RuleV3, split string) *benchV3Counts {
	c := &benchV3Counts{data: benchV3Summary{
		Rule:  benchV3Rule{FHigh: rule.FHigh, FLow: rule.FLow, DocsLow: rule.DocsLow, Threshold: rule.Threshold},
		Split: split, Questions: map[string]benchV3QuestionCounts{},
	}}
	c.code.distribution = map[string]int{}
	c.judge.distribution = map[string]int{}
	for _, lane := range benchComplexityLabels {
		c.code.distribution[lane] = 0
		c.judge.distribution[lane] = 0
	}
	for _, key := range benchV3Questions {
		c.data.Questions[key] = benchV3QuestionCounts{}
	}
	return c
}

func (c *benchV3Counts) add(r benchV3Record) {
	s := &c.data
	s.Tasks++
	if r.OpenMarker {
		s.OpenMarkers++
	}
	if r.InputTokens != nil {
		s.InputTokens += *r.InputTokens
	}
	for _, key := range benchV3Questions {
		count := s.Questions[key]
		if r.Packets[key].Found {
			count.Packets++
			if r.Called {
				count.Calls++
			}
		}
		switch r.Answers[key].Status {
		case "firm":
			count.Firm++
		case "insufficient":
			count.Insufficient++
		case "below_threshold":
			count.BelowThreshold++
		case "unavailable":
			count.Unavailable++
		case "not_asked":
			count.NotAsked++
		}
		s.Questions[key] = count
	}
	switch r.Outcome {
	case benchOutcomeCandidate, benchOutcomeRetried:
		s.Sufficient++
	case benchOutcomeEscalated, benchOutcomeFailed:
		s.Insufficient++
	default:
		s.Unknown++
	}
	c.code.add(r.CodeLane, r.PlanLane, r.Outcome)
	c.judge.add(r.JudgeLane, r.PlanLane, r.Outcome)
}

func (c *benchV3LaneCounts) add(lane, plan, outcome string) {
	if lane == plan {
		c.agreement++
	}
	if outcome == benchOutcomeUnknown {
		return
	}
	c.measured++
	c.distribution[lane]++
	if outcome == benchOutcomeCandidate || outcome == benchOutcomeRetried {
		c.sufficient++
		if benchComplexityRank[routing.Complexity(lane)] <= benchComplexityRank[routing.Complexity(plan)] {
			c.economy++
		}
	} else {
		c.insufficient++
		if benchComplexityRank[routing.Complexity(lane)] > benchComplexityRank[routing.Complexity(plan)] {
			c.safety++
		}
	}
}

func (c *benchV3LaneCounts) summary(tasks int) benchV3LaneSummary {
	s := benchV3LaneSummary{}
	s.Economy = benchV2Measure{Count: c.economy, Total: c.sufficient, Share: benchV2Share(c.economy, c.sufficient)}
	s.Economy.Pass = c.sufficient > 0 && s.Economy.Share >= 0.75
	s.Safety = benchV2Measure{Count: c.safety, Total: c.insufficient, Share: benchV2Share(c.safety, c.insufficient), ReportedOnly: c.insufficient < 10}
	s.Safety.Pass = c.insufficient > 0 && s.Safety.Share >= 0.5
	s.Agreement = benchV2Measure{Count: c.agreement, Total: tasks, Share: benchV2Share(c.agreement, tasks)}
	s.Discrimination.Distribution = c.distribution
	for _, lane := range benchComplexityLabels {
		count := c.distribution[lane]
		if count > 0 {
			s.Discrimination.Lanes++
		}
		if count > c.distribution[s.Discrimination.LargestLane] {
			s.Discrimination.LargestLane = lane
		}
	}
	s.Discrimination.LargestShare = benchV2Share(c.distribution[s.Discrimination.LargestLane], c.measured)
	s.Discrimination.Pass = c.measured > 0 && s.Discrimination.LargestShare <= 0.7 && s.Discrimination.Lanes >= 3
	s.Balance.Share = (s.Economy.Share + s.Safety.Share) / 2
	s.Balance.Pass = c.sufficient > 0 && c.insufficient > 0 && s.Balance.Share >= 0.65
	s.Pass = s.Economy.Pass && (s.Safety.Pass || s.Safety.ReportedOnly) && s.Discrimination.Pass && s.Balance.Pass
	return s
}

func (c *benchV3Counts) summary() benchV3Summary {
	s := c.data
	s.Code = c.code.summary(s.Tasks)
	s.Judge = c.judge.summary(s.Tasks)
	s.BalanceDelta = s.Judge.Balance.Share - s.Code.Balance.Share
	return s
}

func (c *benchV3Counts) printSummary(w io.Writer) error {
	s := c.summary()
	if _, err := fmt.Fprintf(w, "bench v3 tasks=%d sufficient=%d insufficient=%d unknown=%d open_markers=%d input_tokens=%d\n", s.Tasks, s.Sufficient, s.Insufficient, s.Unknown, s.OpenMarkers, s.InputTokens); err != nil {
		return err
	}
	for _, item := range []struct {
		name string
		lane benchV3LaneSummary
	}{{"C", s.Code}, {"J", s.Judge}} {
		lane := item.lane
		safetyNote := ""
		if lane.Safety.ReportedOnly {
			safetyNote = " reported only"
		}
		if _, err := fmt.Fprintf(w, "%s economy=%d/%d (%.2f) %s\n%s safety=%d/%d (%.2f) %s%s\n%s discrimination=largest:%s,share:%.2f,lanes:%d %s\n%s balance=%.2f %s\n%s agreement=%d/%d (%.2f)\n",
			item.name, lane.Economy.Count, lane.Economy.Total, lane.Economy.Share, benchV2Verdict(lane.Economy.Pass),
			item.name, lane.Safety.Count, lane.Safety.Total, lane.Safety.Share, benchV2Verdict(lane.Safety.Pass), safetyNote,
			item.name, lane.Discrimination.LargestLane, lane.Discrimination.LargestShare, lane.Discrimination.Lanes, benchV2Verdict(lane.Discrimination.Pass),
			item.name, lane.Balance.Share, benchV2Verdict(lane.Balance.Pass),
			item.name, lane.Agreement.Count, lane.Agreement.Total, lane.Agreement.Share); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "balance_delta=%.2f\n", s.BalanceDelta); err != nil {
		return err
	}
	for _, key := range benchV3Questions {
		q := s.Questions[key]
		if _, err := fmt.Fprintf(w, "%s packets=%d calls=%d firm=%d insufficient=%d below_threshold=%d unavailable=%d not_asked=%d\n", key, q.Packets, q.Calls, q.Firm, q.Insufficient, q.BelowThreshold, q.Unavailable, q.NotAsked); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "bench v3 C=%s J=%s\n", benchV2Verdict(s.Code.Pass), benchV2Verdict(s.Judge.Pass && s.BalanceDelta >= 0.05))
	return err
}
