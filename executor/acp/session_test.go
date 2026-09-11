package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func configState(model, effort string) string {
	return fmt.Sprintf(`{"configOptions":[{"id":"models","category":"model","type":"select","currentValue":%q,"options":[{"value":"small"},{"value":"large"}]},{"id":"reasoning","category":"thought_level","type":"select","currentValue":%q,"options":[{"value":"low"},{"value":"high"}]}]}`, model, effort)
}

func sessionReply(t *testing.T, peer io.Writer, request map[string]json.RawMessage, result string) {
	t.Helper()
	writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, request["id"], result))
}

func expectMethod(t *testing.T, reader *bufio.Reader, method string) map[string]json.RawMessage {
	t.Helper()
	request := readMessage(t, reader)
	if string(request["method"]) != fmt.Sprintf("%q", method) {
		t.Fatalf("method = %s, want %s", request["method"], method)
	}
	return request
}

func setupPeer(t *testing.T, peer io.ReadWriter, reader *bufio.Reader, cwd, state string) {
	t.Helper()
	request := expectMethod(t, reader, "initialize")
	var init struct {
		ClientCapabilities map[string]json.RawMessage `json:"clientCapabilities"`
	}
	if err := json.Unmarshal(request["params"], &init); err != nil || len(init.ClientCapabilities) != 0 {
		t.Fatalf("capabilities: %s", request["params"])
	}
	sessionReply(t, peer, request, `{"protocolVersion":1,"agentCapabilities":{}}`)
	request = expectMethod(t, reader, "session/new")
	var params struct {
		Cwd        string            `json:"cwd"`
		MCPServers []json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(request["params"], &params); err != nil || params.Cwd != cwd || params.MCPServers == nil || len(params.MCPServers) != 0 {
		t.Fatalf("session/new: %s", request["params"])
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(state), &result); err != nil || result == nil {
		t.Fatalf("invalid session state: %s", state)
	}
	result["sessionId"] = json.RawMessage(`"task"`)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	sessionReply(t, peer, request, string(encoded))
}

func TestSessionAcknowledgesConfigurationBeforePrompt(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	cwd := t.TempDir() + "/../exact workspace"
	done := make(chan error, 1)
	go func() {
		session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, Model: "large", Effort: "high"})
		if err == nil {
			var result TurnResult
			result, err = session.Prompt(context.Background(), "the brief", nil)
			if err == nil && (!result.Completed || result.StopReason != "end_turn") {
				err = fmt.Errorf("turn: %+v", result)
			}
		}
		done <- err
	}()
	reader := bufio.NewReader(peer)
	setupPeer(t, peer, reader, cwd, configState("small", "low"))
	for _, selection := range []struct{ id, value, model, effort string }{{"models", "large", "large", "low"}, {"reasoning", "high", "large", "high"}} {
		request := expectMethod(t, reader, "session/set_config_option")
		var params struct {
			SessionID string `json:"sessionId"`
			ConfigID  string `json:"configId"`
			Value     string `json:"value"`
		}
		if json.Unmarshal(request["params"], &params) != nil || params.SessionID != "task" || params.ConfigID != selection.id || params.Value != selection.value {
			t.Fatalf("config: %s", request["params"])
		}
		sessionReply(t, peer, request, configState(selection.model, selection.effort))
	}
	request := expectMethod(t, reader, "session/prompt")
	if string(request["params"]) != `{"sessionId":"task","prompt":[{"type":"text","text":"the brief"}]}` {
		t.Fatalf("prompt: %s", request["params"])
	}
	sessionReply(t, peer, request, `{"stopReason":"end_turn"}`)
	awaitError(t, done, nil)
}

func TestSessionRejectsIncompatibleConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, model, effort, state, ack string }{
		{name: "missing model", model: "large", state: `{}`},
		{name: "unknown model", model: "invented", state: configState("small", "low")},
		{name: "missing effort", effort: "high", state: `{}`},
		{name: "unacknowledged model", model: "large", state: configState("small", "low"), ack: configState("small", "low")},
		{name: "effort resets model", model: "large", effort: "high", state: configState("large", "low"), ack: configState("small", "high")},
		{name: "empty acknowledgement", model: "large", state: configState("small", "low"), ack: `{}`},
		{name: "ambiguous category", model: "large", state: `{"configOptions":[{"id":"first","category":"model","type":"select","currentValue":"large","options":[{"value":"large"}]},{"id":"second","category":"model","type":"select","currentValue":"large","options":[{"value":"large"}]}]}`},
		{name: "unsupported option type", model: "large", state: `{"configOptions":[{"id":"models","category":"model","type":"text","currentValue":"large","options":[{"value":"large"}]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, peer := testConnection(t, Options{})
			cwd := t.TempDir()
			done := make(chan error, 1)
			go func() {
				_, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, Model: tt.model, Effort: tt.effort})
				done <- err
			}()
			reader := bufio.NewReader(peer)
			setupPeer(t, peer, reader, cwd, tt.state)
			if tt.ack != "" {
				request := expectMethod(t, reader, "session/set_config_option")
				sessionReply(t, peer, request, tt.ack)
			}
			awaitError(t, done, ErrConfiguration)
			if conn.nextID > 4 {
				t.Fatalf("unexpected request submitted: %d", conn.nextID)
			}
		})
	}
}

func TestSessionSelectsExplicitIDsAndGroupedValues(t *testing.T) {
	conn, peer := testConnection(t, Options{})
	cwd := t.TempDir()
	done := make(chan error, 1)
	go func() {
		session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, Model: "large", Effort: "high", ModelConfigID: "vendor-model", EffortConfigID: "vendor-effort"})
		if err == nil {
			_, err = session.Prompt(context.Background(), "brief", nil)
		}
		done <- err
	}()
	reader := bufio.NewReader(peer)
	state := `{"configOptions":[{"id":"vendor-model","type":"select","currentValue":"large","options":[{"group":"family","options":[{"value":"large"}]}]},{"id":"vendor-effort","type":"select","currentValue":"high","options":[{"value":"high"}]}]}`
	setupPeer(t, peer, reader, cwd, state)
	// The session/new state already acknowledges both requested selections.
	request := expectMethod(t, reader, "session/prompt")
	sessionReply(t, peer, request, `{"stopReason":"end_turn"}`)
	awaitError(t, done, nil)
}

func TestSessionRejectsConfigurationDriftDuringPrompt(t *testing.T) {
	conn, peer := testConnection(t, Options{})
	cwd := t.TempDir()
	done := make(chan error, 1)
	go func() {
		session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, Model: "large", Effort: "high"})
		if err == nil {
			var result TurnResult
			result, err = session.Prompt(context.Background(), "brief", nil)
			if result.Completed || !result.SubmissionAttempted {
				err = fmt.Errorf("configuration drift turn: %+v / %v", result, err)
			}
		}
		done <- err
	}()
	reader := bufio.NewReader(peer)
	setupPeer(t, peer, reader, cwd, configState("large", "high"))
	expectMethod(t, reader, "session/prompt")
	writeMessage(t, peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"config_option_update",`+strings.TrimPrefix(configState("small", "high"), "{")+`}}`)
	awaitError(t, done, ErrConfiguration)
}

func TestSessionRejectsOversizedPromptBeforeSubmission(t *testing.T) {
	conn, peer := testConnection(t, Options{MaxFrameBytes: 512})
	cwd := t.TempDir()
	done := make(chan error, 1)
	go func() {
		session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, Model: "default"})
		if err == nil {
			var result TurnResult
			// Escaping pushes the encoded request over the frame limit.
			result, err = session.Prompt(context.Background(), "brief"+strings.Repeat("\n", 240), nil)
			if result.SubmissionAttempted {
				err = fmt.Errorf("oversized prompt submitted: %+v / %v", result, err)
			}
		}
		done <- err
	}()
	reader := bufio.NewReader(peer)
	setupPeer(t, peer, reader, cwd, `{}`)
	awaitError(t, done, ErrFrameTooLarge)
}

func TestSessionRejectsRelativeCwdBeforeInitialize(t *testing.T) {
	conn, _ := testConnection(t, Options{})
	_, err := NewSession(context.Background(), conn, SessionConfig{Cwd: "relative"})
	if !errors.Is(err, ErrConfiguration) || conn.initialized {
		t.Fatalf("relative cwd: %v, initialized %v", err, conn.initialized)
	}
}

func TestSessionStreamsOnlyAgentTextAndCannotReplay(t *testing.T) {
	conn, peer := testConnection(t, Options{})
	cwd := t.TempDir()
	type outcome struct {
		result TurnResult
		text   string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd})
		var result TurnResult
		var text strings.Builder
		if err == nil {
			result, err = session.Prompt(context.Background(), "brief", func(chunk string) error { _, e := text.WriteString(chunk); return e })
			if _, again := session.Prompt(context.Background(), "replay", nil); !errors.Is(again, ErrAlreadyPrompted) {
				err = fmt.Errorf("replay: %v", again)
			}
		}
		done <- outcome{result, text.String(), err}
	}()
	reader := bufio.NewReader(peer)
	setupPeer(t, peer, reader, cwd, `{}`)
	request := expectMethod(t, reader, "session/prompt")
	for _, update := range []string{
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"reasoning-canary"}}`,
		`{"sessionUpdate":"tool_call","title":"BATUTA-PROGRESS 9 DONE","rawInput":{"token":"credential-canary"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello "}}`,
		`{"sessionUpdate":"usage_update","used":300,"size":1000}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"world"}}`,
	} {
		writeMessage(t, peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":`+update+`}}`)
	}
	sessionReply(t, peer, request, `{"stopReason":"end_turn","usage":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40}}`)
	got := <-done
	if got.err != nil || got.text != "hello world" || !got.result.Completed || !got.result.SubmissionAttempted || got.result.Usage == nil || *got.result.Usage.InputTokens != 100 || *got.result.Usage.CachedInputTokens != 40 {
		t.Fatalf("turn: %+v", got)
	}
}
