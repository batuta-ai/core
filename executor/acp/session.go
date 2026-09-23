package acp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

var (
	ErrConfiguration    = errors.New("acp: requested session configuration unavailable or unacknowledged")
	ErrAlreadyPrompted  = errors.New("acp: prompt already attempted")
	ErrPermissionDenied = errors.New("acp: permission requires an explicit policy")
	ErrOutput           = errors.New("acp: output sink failed")
)

// SessionConfig selects exact wire values. Explicit IDs support agents that
// omit semantic categories; display names are never used to guess a setting.
type SessionConfig struct {
	Cwd              string
	Model            string
	Effort           string
	ModelConfigID    string
	EffortConfigID   string
	ClientInfo       Implementation
	PermissionPolicy PermissionPolicy
}

// Session is one configured attempt. It neither authenticates nor owns the
// worker process. The owner must close the connection and verify worker exit.
type Session struct {
	conn          *Connection
	id            string
	config        SessionConfig
	options       []ConfigOption
	prompted      atomic.Bool
	enforcing     bool
	skippedEffort bool
}

type TurnResult struct {
	SubmissionAttempted bool
	Completed           bool
	StopReason          string
	Usage               *Usage
}

// CacheSemantics says how cached counters relate to the input counter.
type CacheSemantics string

// ACP reports cached reads and writes on top of the input counter.
const CacheSemanticsAdditive CacheSemantics = "additive"

// Usage is optional peer-reported accounting. Nil counters are unknown;
// context occupancy is not token consumption, and cost is copied from the
// agent, never computed.
type Usage struct {
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheWriteTokens    *int64
	ReasoningTokens     *int64
	ReportedTotalTokens *int64
	CostAmount          *float64
	CostCurrency        string
	CacheSemantics      CacheSemantics
	Provenance          string
}

func NewSession(ctx context.Context, conn *Connection, config SessionConfig) (*Session, error) {
	if conn == nil || !filepath.IsAbs(config.Cwd) {
		return nil, ErrConfiguration
	}
	if config.Model == "default" {
		config.Model = ""
	}
	if config.ClientInfo.Name == "" {
		config.ClientInfo = Implementation{Name: "batuta", Version: "1"}
	}
	conn.mu.Lock()
	if conn.initialized {
		conn.mu.Unlock()
		return nil, ErrAlreadyInitialized
	}
	conn.ordered = true
	conn.mu.Unlock()
	if _, err := conn.Initialize(ctx, config.ClientInfo); err != nil {
		return nil, err
	}
	session := &Session{conn: conn, config: config}
	var state sessionState
	params, _ := json.Marshal(struct {
		Cwd        string     `json:"cwd"`
		MCPServers []struct{} `json:"mcpServers"`
	}{config.Cwd, []struct{}{}})
	_, err := session.call(ctx, "session/new", params, nil, nil, func(raw json.RawMessage) error {
		if json.Unmarshal(raw, &state) != nil || strings.TrimSpace(state.SessionID) == "" {
			return ErrProtocol
		}
		session.id = state.SessionID
		session.options = state.ConfigOptions
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := session.selectOption(ctx, "model", config.ModelConfigID, config.Model); err != nil {
		return nil, err
	}
	if err := session.selectOption(ctx, "thought_level", config.EffortConfigID, config.Effort); err != nil {
		if !session.effortNotApplicable(err) {
			return nil, err
		}
		session.skippedEffort = true
	}
	if err := session.checkConfiguration(); err != nil {
		return nil, err
	}
	session.enforcing = true
	return session, nil
}

func (s *Session) option(category, id, value string) (ConfigOption, error) {
	var found *ConfigOption
	ids := make(map[string]bool, len(s.options))
	for _, option := range s.options {
		if option.ID == "" || ids[option.ID] {
			return ConfigOption{}, ErrConfiguration
		}
		ids[option.ID] = true
		if (id != "" && option.ID == id) || (id == "" && option.Category == category) {
			if found != nil {
				return ConfigOption{}, ErrConfiguration
			}
			copy := option
			found = &copy
		}
	}
	if found == nil || found.Type != "select" {
		return ConfigOption{}, ErrConfiguration
	}
	for _, candidate := range found.Options {
		if candidate.Value == value {
			return *found, nil
		}
		for _, grouped := range candidate.Options {
			if grouped.Value == value {
				return *found, nil
			}
		}
	}
	return ConfigOption{}, ErrConfiguration
}

func (s *Session) selectOption(ctx context.Context, category, id, value string) error {
	if value == "" {
		return nil
	}
	option, err := s.option(category, id, value)
	if err != nil {
		return err
	}
	var current string
	if json.Unmarshal(option.CurrentValue, &current) == nil && current == value {
		return nil
	}
	params, _ := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		ConfigID  string `json:"configId"`
		Value     string `json:"value"`
	}{s.id, option.ID, value})
	_, err = s.call(ctx, "session/set_config_option", params, nil, nil, func(raw json.RawMessage) error {
		var state sessionState
		if json.Unmarshal(raw, &state) != nil {
			return ErrProtocol
		}
		s.options = state.ConfigOptions
		return s.checkOption(category, id, value)
	})
	if err != nil {
		return err
	}
	return s.checkOption(category, id, value)
}

func (s *Session) checkOption(category, id, value string) error {
	if value == "" {
		return nil
	}
	option, err := s.option(category, id, value)
	if err != nil {
		return err
	}
	var current string
	if json.Unmarshal(option.CurrentValue, &current) != nil || current != value {
		return ErrConfiguration
	}
	return nil
}

func (s *Session) hasOption(category, id string) bool {
	for _, option := range s.options {
		if (id != "" && option.ID == id) || (id == "" && option.Category == category) {
			return true
		}
	}
	return false
}

func (s *Session) effortNotApplicable(err error) bool {
	return err != nil && s.config.EffortConfigID == "" && !s.hasOption("thought_level", s.config.EffortConfigID)
}

func (s *Session) EffortNotApplicable() bool {
	return s.skippedEffort
}

func (s *Session) checkConfiguration() error {
	if err := s.checkOption("model", s.config.ModelConfigID, s.config.Model); err != nil {
		return err
	}
	if err := s.checkOption("thought_level", s.config.EffortConfigID, s.config.Effort); err != nil {
		if s.enforcing {
			if s.skippedEffort && !s.hasOption("thought_level", s.config.EffortConfigID) {
				return nil
			}
			return err
		}
		if s.effortNotApplicable(err) {
			return nil
		}
		return err
	}
	return nil
}

// Prompt may be called once, even if submission fails. A transport failure in
// Call's send/reply window cannot prove that the worker did not accept work.
// Text callbacks are serialized and must return promptly.
func (s *Session) Prompt(ctx context.Context, prompt string, text func(string) error) (TurnResult, error) {
	result := TurnResult{}
	if !s.prompted.CompareAndSwap(false, true) {
		return result, ErrAlreadyPrompted
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(prompt) == "" {
		return result, ErrConfiguration
	}
	if len(prompt)+len(s.id) > s.conn.options.MaxFrameBytes {
		return result, ErrFrameTooLarge
	}
	if err := s.drain(ctx, nil, nil); err != nil {
		return result, err
	}
	params, _ := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}{s.id, []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{"text", prompt}}})
	// Include JSON escaping and envelope overhead in the pre-submission check.
	if _, err := s.conn.encode(message{JSONRPC: "2.0", ID: json.RawMessage(`"batuta-18446744073709551615"`), Method: "session/prompt", Params: params}); err != nil {
		return result, err
	}
	if err := s.conn.Err(); err != nil {
		return result, err
	}
	result.SubmissionAttempted = true
	raw, err := s.call(ctx, "session/prompt", params, text, &result, nil)
	if err != nil {
		return result, err
	}
	var response promptResponse
	if json.Unmarshal(raw, &response) != nil || response.StopReason == "" {
		return result, ErrProtocol
	}
	result.Completed = true
	switch response.StopReason {
	case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled":
		result.StopReason = response.StopReason
	default:
		result.StopReason = "unknown"
	}
	if usage := decodeUsage(response.Usage); usage != nil {
		// The cost a usage_update reported survives the decoded response usage.
		if result.Usage != nil {
			usage.CostAmount = result.Usage.CostAmount
			usage.CostCurrency = result.Usage.CostCurrency
		}
		result.Usage = usage
	}
	return result, nil
}

func (s *Session) call(ctx context.Context, method string, params json.RawMessage, text func(string) error, result *TurnResult, apply func(json.RawMessage) error) (json.RawMessage, error) {
	// Keep the transport alive briefly to send cancellation before making the
	// attempt terminal. This loop owns the task budget; the connection bounds
	// control replies and every write independently.
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	replies := make(chan reply, 1)
	go func() { raw, err := s.conn.call(callCtx, method, params, true); replies <- reply{raw, err} }()
	events := s.conn.events
	var responseErr error
	handle := func(event message) error {
		if responseErr != nil {
			return responseErr
		}
		if event.Method == "" {
			if event.Error == nil && apply != nil {
				responseErr = apply(event.Result)
			}
			return nil
		}
		return s.event(ctx, event, text, result)
	}
	stop := func() (json.RawMessage, error) {
		if method == "session/prompt" {
			cancelCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
			payload, _ := json.Marshal(struct {
				SessionID string `json:"sessionId"`
			}{s.id})
			_ = s.conn.Notify(cancelCtx, "session/cancel", payload)
			finish()
		}
		cancel()
		s.conn.fail(ctx.Err())
		return nil, ctx.Err()
	}
	for {
		if ctx.Err() != nil {
			_, err := stop()
			<-replies
			return nil, err
		}
		select {
		case <-ctx.Done():
			_, err := stop()
			<-replies
			return nil, err
		case got := <-replies:
			if ctx.Err() != nil {
				return stop()
			}
			if err := s.drainEvents(handle); err != nil {
				return nil, err
			}
			if responseErr != nil {
				return nil, responseErr
			}
			return got.result, got.err
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := handle(event); err != nil {
				s.conn.fail(err)
				<-replies
				return nil, err
			}
			if responseErr != nil {
				events = nil
			}
		}
	}
}

// A completed Call can overtake its buffered events in the caller's select.
func (s *Session) drainEvents(handle func(message) error) error {
	for range len(s.conn.events) {
		event, ok := <-s.conn.events
		if !ok {
			break
		}
		if err := handle(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) drain(ctx context.Context, text func(string) error, result *TurnResult) error {
	return s.drainEvents(func(event message) error { return s.event(ctx, event, text, result) })
}

func (s *Session) event(ctx context.Context, event message, text func(string) error, result *TurnResult) error {
	if event.Method == "session/update" {
		s.conn.mu.Lock()
		s.conn.queuedUpdates--
		s.conn.mu.Unlock()
		return s.update(Notification{Method: event.Method, Params: event.Params}, text, result)
	}
	if event.Method == "session/request_permission" {
		return s.permission(ctx, Request{ID: event.ID, Method: event.Method, Params: event.Params}, result != nil)
	}
	return ErrProtocol
}

func (s *Session) update(notification Notification, text func(string) error, result *TurnResult) error {
	var params struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(notification.Params, &params) != nil || params.SessionID != s.id {
		return ErrProtocol
	}
	var update struct {
		Kind          string          `json:"sessionUpdate"`
		Content       json.RawMessage `json:"content"`
		ConfigOptions []ConfigOption  `json:"configOptions"`
	}
	if json.Unmarshal(params.Update, &update) != nil || update.Kind == "" {
		return ErrProtocol
	}
	switch update.Kind {
	case "agent_message_chunk":
		var content struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(update.Content, &content) != nil {
			return ErrProtocol
		}
		if content.Type == "text" {
			if content.Text == nil {
				return ErrProtocol
			}
			if text != nil {
				if err := text(*content.Text); err != nil {
					return ErrOutput
				}
			}
		}
	case "config_option_update":
		s.options = update.ConfigOptions
		if s.enforcing {
			return s.checkConfiguration()
		}
	case "usage_update":
		var state struct {
			Cost *struct {
				Amount   *float64 `json:"amount"`
				Currency *string  `json:"currency"`
			} `json:"cost"`
		}
		// The draft usage surface is informational: a cost the agent does not
		// express as {amount, currency} is absent, never invented.
		if json.Unmarshal(params.Update, &state) != nil {
			state.Cost = nil
		}
		if result != nil {
			if result.Usage == nil {
				result.Usage = &Usage{Provenance: "acp/session-update"}
			}
			if state.Cost != nil {
				result.Usage.CostAmount = state.Cost.Amount
				if state.Cost.Currency != nil {
					result.Usage.CostCurrency = *state.Cost.Currency
				}
			}
		}
	}
	return nil
}

func decodeUsage(raw json.RawMessage) *Usage {
	// This optional draft field is not a negotiated stable ACP capability.
	// Retain only explicit non-negative counters, never context occupancy.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil
	}
	counter := func(key string) *int64 {
		var value *int64
		if json.Unmarshal(fields[key], &value) != nil || value == nil || *value < 0 {
			return nil
		}
		return value
	}
	return &Usage{
		InputTokens:         counter("inputTokens"),
		OutputTokens:        counter("outputTokens"),
		CacheReadTokens:     counter("cachedReadTokens"),
		CacheWriteTokens:    counter("cachedWriteTokens"),
		ReasoningTokens:     counter("thoughtTokens"),
		ReportedTotalTokens: counter("totalTokens"),
		CacheSemantics:      CacheSemanticsAdditive,
		Provenance:          "acp/session-prompt/usage (draft)",
	}
}
