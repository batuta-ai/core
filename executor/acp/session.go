package acp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	conn      *Connection
	id        string
	config    SessionConfig
	options   []ConfigOption
	prompted  atomic.Bool
	enforcing bool
}

type TurnResult struct {
	SubmissionAttempted bool
	Completed           bool
	StopReason          string
	Usage               *TokenUsage
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
	if _, err := conn.Initialize(ctx, config.ClientInfo); err != nil {
		return nil, err
	}
	session := &Session{conn: conn, config: config}
	var state sessionState
	params, _ := json.Marshal(struct {
		Cwd        string     `json:"cwd"`
		MCPServers []struct{} `json:"mcpServers"`
	}{config.Cwd, []struct{}{}})
	raw, err := conn.Call(ctx, "session/new", params)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &state) != nil || strings.TrimSpace(state.SessionID) == "" {
		return nil, ErrProtocol
	}
	session.id = state.SessionID
	session.options = state.ConfigOptions
	if err := session.drain(ctx, nil, nil); err != nil {
		return nil, err
	}
	if err := session.selectOption(ctx, "model", config.ModelConfigID, config.Model); err != nil {
		return nil, err
	}
	if err := session.selectOption(ctx, "thought_level", config.EffortConfigID, config.Effort); err != nil {
		return nil, err
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
	raw, err := s.call(ctx, "session/set_config_option", params, nil, nil)
	if err != nil {
		return err
	}
	var state sessionState
	if json.Unmarshal(raw, &state) != nil {
		return ErrProtocol
	}
	s.options = state.ConfigOptions
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

func (s *Session) checkConfiguration() error {
	if err := s.checkOption("model", s.config.ModelConfigID, s.config.Model); err != nil {
		return err
	}
	return s.checkOption("thought_level", s.config.EffortConfigID, s.config.Effort)
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
	raw, err := s.call(ctx, "session/prompt", params, text, &result)
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
		result.Usage = usage
	}
	return result, nil
}

func (s *Session) call(ctx context.Context, method string, params json.RawMessage, text func(string) error, result *TurnResult) (json.RawMessage, error) {
	replies := make(chan reply, 1)
	go func() { raw, err := s.conn.Call(ctx, method, params); replies <- reply{raw, err} }()
	notifications, requests := s.conn.Notifications(), s.conn.Requests()
	for {
		select {
		case got := <-replies:
			if err := s.drain(ctx, text, result); err != nil {
				return nil, err
			}
			return got.result, got.err
		case notification, ok := <-notifications:
			if !ok {
				notifications = nil
				continue
			}
			if err := s.update(notification, text, result); err != nil {
				s.conn.fail(err)
				<-replies
				return nil, err
			}
		case request, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			if err := s.permission(ctx, request, result != nil); err != nil {
				s.conn.fail(err)
				<-replies
				return nil, err
			}
		}
	}
}

// A reply can overtake buffered notifications in the caller's select. Drain
// the bounded queue before returning so preceding chunks are not lost.
func (s *Session) drain(ctx context.Context, text func(string) error, result *TurnResult) error {
	for range len(s.conn.Notifications()) {
		notification, ok := <-s.conn.Notifications()
		if !ok {
			break
		}
		if err := s.update(notification, text, result); err != nil {
			return err
		}
	}
	for range len(s.conn.Requests()) {
		request, ok := <-s.conn.Requests()
		if !ok {
			break
		}
		if err := s.permission(ctx, request, result != nil); err != nil {
			return err
		}
	}
	return nil
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
		if result != nil && result.Usage == nil {
			result.Usage = &TokenUsage{Provenance: "acp/session-update"}
		}
	}
	return nil
}

func decodeUsage(raw json.RawMessage) *TokenUsage {
	// This optional draft field is not a negotiated stable ACP capability.
	// Retain only explicit non-negative counters, never context size or totals.
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
	return &TokenUsage{InputTokens: counter("inputTokens"), CachedInputTokens: counter("cachedReadTokens"), OutputTokens: counter("outputTokens"), Provenance: "acp/session-prompt/usage (draft)"}
}
