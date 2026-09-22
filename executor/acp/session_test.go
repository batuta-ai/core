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
	"testing/synctest"
	"time"
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

func TestSessionRefusesUnconfirmedModel(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, model, state, ack string }{
		{name: "not advertised", model: "invented", state: configState("small", "low")},
		{name: "unconfirmed before prompt", model: "large", state: configState("small", "low"), ack: configState("small", "low")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn, peer := testConnection(t, Options{})
			cwd := t.TempDir()
			done := make(chan error, 1)
			go func() {
				_, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, Model: tt.model})
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

func TestSessionRejectsIncompatibleConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, model, effort, state, ack string }{
		{name: "missing model", model: "large", state: `{}`},
		{name: "unknown model", model: "invented", state: configState("small", "low")},
		{name: "unacknowledged model", model: "large", state: configState("small", "low"), ack: configState("small", "low")},
		{name: "effort resets model", model: "large", effort: "high", state: configState("large", "low"), ack: configState("small", "high")},
		{name: "effort not offered", model: "large", effort: "high", state: `{"configOptions":[{"id":"models","category":"model","type":"select","currentValue":"large","options":[{"value":"large"}]},{"id":"reasoning","category":"thought_level","type":"select","currentValue":"low","options":[{"value":"low"}]}]}`},
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

func TestSessionEffortNotApplicableWithoutThoughtLevel(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	cwd := t.TempDir()
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
	setupPeer(t, peer, reader, cwd, `{"configOptions":[{"id":"models","category":"model","type":"select","currentValue":"small","options":[{"value":"small"},{"value":"large"}]}]}`)
	// The only selection is the model: with no thought_level option and no
	// EffortConfigID, the requested effort is not_applicable.
	request := expectMethod(t, reader, "session/set_config_option")
	var params struct {
		SessionID string `json:"sessionId"`
		ConfigID  string `json:"configId"`
		Value     string `json:"value"`
	}
	if json.Unmarshal(request["params"], &params) != nil || params.SessionID != "task" || params.ConfigID != "models" || params.Value != "large" {
		t.Fatalf("config: %s", request["params"])
	}
	sessionReply(t, peer, request, `{"configOptions":[{"id":"models","category":"model","type":"select","currentValue":"large","options":[{"value":"small"},{"value":"large"}]}]}`)
	request = expectMethod(t, reader, "session/prompt")
	if string(request["params"]) != `{"sessionId":"task","prompt":[{"type":"text","text":"the brief"}]}` {
		t.Fatalf("prompt: %s", request["params"])
	}
	sessionReply(t, peer, request, `{"stopReason":"end_turn"}`)
	awaitError(t, done, nil)
	if conn.nextID > 4 {
		t.Fatalf("unexpected effort selection submitted: %d", conn.nextID)
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
	if got.err != nil || got.text != "hello world" || !got.result.Completed || !got.result.SubmissionAttempted || got.result.Usage == nil || *got.result.Usage.InputTokens != 100 || *got.result.Usage.CacheReadTokens != 40 {
		t.Fatalf("turn: %+v", got)
	}
}

func runUsageTurn(t *testing.T, update, response string) TurnResult {
	t.Helper()
	conn, peer := testConnection(t, Options{})
	cwd := t.TempDir()
	type outcome struct {
		result TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd})
		var result TurnResult
		if err == nil {
			result, err = session.Prompt(context.Background(), "brief", nil)
		}
		done <- outcome{result, err}
	}()
	reader := bufio.NewReader(peer)
	setupPeer(t, peer, reader, cwd, `{}`)
	request := expectMethod(t, reader, "session/prompt")
	writeMessage(t, peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":`+update+`}}`)
	sessionReply(t, peer, request, response)
	got := <-done
	if got.err != nil || !got.result.Completed {
		t.Fatalf("turn: %+v / %v", got.result, got.err)
	}
	return got.result
}

func TestDecodeUsageAllCounters(t *testing.T) {
	t.Run("keeps every counter with the reported cost", func(t *testing.T) {
		t.Parallel()
		usage := runUsageTurn(t,
			`{"sessionUpdate":"usage_update","used":300,"size":1000,"cost":{"amount":0.25,"currency":"EUR"}}`,
			`{"stopReason":"end_turn","usage":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"cachedWriteTokens":5,"thoughtTokens":8,"totalTokens":173}}`).Usage
		if usage == nil {
			t.Fatal("no usage recorded")
		}
		if usage.InputTokens == nil || *usage.InputTokens != 100 || usage.OutputTokens == nil || *usage.OutputTokens != 20 ||
			usage.CacheReadTokens == nil || *usage.CacheReadTokens != 40 || usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 5 ||
			usage.ReasoningTokens == nil || *usage.ReasoningTokens != 8 || usage.ReportedTotalTokens == nil || *usage.ReportedTotalTokens != 173 {
			t.Fatalf("counters: %+v", usage)
		}
		if usage.CostAmount == nil || *usage.CostAmount != 0.25 || usage.CostCurrency != "EUR" {
			t.Fatalf("cost: %+v", usage)
		}
		if usage.CacheSemantics != CacheSemanticsAdditive {
			t.Fatalf("cache semantics: %q", usage.CacheSemantics)
		}
		if usage.Provenance != "acp/session-prompt/usage (draft)" {
			t.Fatalf("provenance: %q", usage.Provenance)
		}
	})
	t.Run("absent cost and unusable counters stay nil", func(t *testing.T) {
		t.Parallel()
		usage := runUsageTurn(t,
			`{"sessionUpdate":"usage_update","used":300,"size":1000}`,
			`{"stopReason":"end_turn","usage":{"inputTokens":-1,"outputTokens":1.5,"cachedReadTokens":"credential-canary","cachedWriteTokens":null,"thoughtTokens":-2,"totalTokens":false}}`).Usage
		if usage == nil {
			t.Fatal("no usage recorded")
		}
		if usage.InputTokens != nil || usage.OutputTokens != nil || usage.CacheReadTokens != nil || usage.CacheWriteTokens != nil ||
			usage.ReasoningTokens != nil || usage.ReportedTotalTokens != nil || usage.CostAmount != nil || usage.CostCurrency != "" {
			t.Fatalf("invented accounting: %+v", usage)
		}
	})
}

func TestSessionCancellationNotifiesAndRejectsLateResult(t *testing.T) {
	for _, lateResult := range []bool{false, true} {
		t.Run(fmt.Sprint(lateResult), func(t *testing.T) {
			conn, peer := testConnection(t, Options{})
			cwd := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				session, err := NewSession(ctx, conn, SessionConfig{Cwd: cwd})
				if err == nil {
					var result TurnResult
					result, err = session.Prompt(ctx, "brief", nil)
					if result.Completed || !result.SubmissionAttempted {
						err = fmt.Errorf("cancelled turn: %+v / %v", result, err)
					}
				}
				done <- err
			}()
			reader := bufio.NewReader(peer)
			setupPeer(t, peer, reader, cwd, `{}`)
			prompt := expectMethod(t, reader, "session/prompt")
			cancel()
			request := expectMethod(t, reader, "session/cancel")
			if len(request["id"]) != 0 || string(request["params"]) != `{"sessionId":"task"}` {
				t.Fatalf("cancel notification: %v", request)
			}
			if lateResult {
				// Closing can race this response: neither outcome may revive the turn.
				fmt.Fprintf(peer, `{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", prompt["id"])
			}
			awaitError(t, done, context.Canceled)
		})
	}
}

func TestSessionCancellationBoundsBlockedCancelWrite(t *testing.T) {
	conn, peer := testConnection(t, Options{})
	cwd := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		session, err := NewSession(ctx, conn, SessionConfig{Cwd: cwd})
		if err == nil {
			_, err = session.Prompt(ctx, "brief", nil)
		}
		done <- err
	}()
	reader := bufio.NewReader(peer)
	setupPeer(t, peer, reader, cwd, `{}`)
	expectMethod(t, reader, "session/prompt")
	cancel()
	// net.Pipe blocks the cancel write because the peer never reads again.
	awaitError(t, done, context.Canceled)
	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("blocked cancel retained transport goroutines")
	}
}

func TestSessionPromptUsesTaskBudget(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn, peer := testConnection(t, Options{MaxPending: 1, RequestTimeout: 100 * time.Millisecond})
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				cwd := t.TempDir()
				done := make(chan error, 1)
				go func() {
					session, err := NewSession(ctx, conn, SessionConfig{Cwd: cwd, PermissionPolicy: func(context.Context, PermissionRequest) string { return "yes" }})
					if err == nil {
						var result TurnResult
						result, err = session.Prompt(ctx, "brief", nil)
						if !result.SubmissionAttempted || result.Completed != complete {
							err = fmt.Errorf("turn: %+v / %v", result, err)
						}
					}
					done <- err
				}()
				reader := bufio.NewReader(peer)
				setupPeer(t, peer, reader, cwd, "{}")
				prompt := expectMethod(t, reader, "session/prompt")
				<-time.NewTimer(200 * time.Millisecond).C
				synctest.Wait()
				if err := conn.Err(); err != nil {
					t.Fatalf("prompt terminated at control timeout: %v", err)
				}
				writeMessage(t, peer, `{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":`+permissionParams+`}`)
				response := readMessage(t, reader)
				if string(response["result"]) != `{"outcome":{"outcome":"selected","optionId":"yes"}}` {
					t.Fatalf("permission response: %s", response)
				}
				if complete {
					sessionReply(t, peer, prompt, `{"stopReason":"end_turn"}`)
					awaitError(t, done, nil)
				} else {
					<-ctx.Done()
					expectMethod(t, reader, "session/cancel")
					awaitError(t, done, context.DeadlineExceeded)
					<-conn.Done()
				}
			})
		})
	}
}
