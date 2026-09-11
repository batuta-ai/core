// Package acp implements the bounded JSON-RPC transport for an ACP v1 client.
package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"
)

const ProtocolVersion = 1

type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         Implementation    `json:"agentInfo"`
	AuthMethods       []AuthMethod      `json:"authMethods"`
}

type AgentCapabilities struct {
	LoadSession        bool `json:"loadSession"`
	PromptCapabilities struct {
		Image           bool `json:"image"`
		Audio           bool `json:"audio"`
		EmbeddedContext bool `json:"embeddedContext"`
	} `json:"promptCapabilities"`
	MCPCapabilities struct {
		HTTP bool `json:"http"`
		SSE  bool `json:"sse"`
	} `json:"mcpCapabilities"`
	SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities"`
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// RPCError retains only the peer's error code. Untrusted messages and data must
// not become diagnostics containing credentials or agent reasoning.
type RPCError struct{ Code int }

func (e *RPCError) Error() string { return fmt.Sprintf("acp: remote error (%d)", e.Code) }

type Notification struct {
	Method string
	Params json.RawMessage
}

// Request is a permission request awaiting an explicit response. Merely reading
// it does not authorize the action. Respond must run before RequestTimeout.
type Request struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

func decodeMessage(frame []byte) (message, error) {
	var msg message
	if !utf8.Valid(frame) || !json.Valid(frame) {
		return msg, ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(frame))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return msg, ErrProtocol
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return msg, ErrProtocol
		}
		key, ok := token.(string)
		if !ok {
			return msg, ErrProtocol
		}
		if _, found := fields[key]; found {
			return msg, ErrProtocol
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return msg, ErrProtocol
		}
		fields[key] = value
	}
	if json.Unmarshal(fields["jsonrpc"], &msg.JSONRPC) != nil || msg.JSONRPC != "2.0" {
		return msg, ErrProtocol
	}
	msg.ID = fields["id"]
	msg.Params = fields["params"]
	msg.Result = fields["result"]
	if method, found := fields["method"]; found {
		if json.Unmarshal(method, &msg.Method) != nil {
			return msg, ErrProtocol
		}
	}
	_, hasID := fields["id"]
	_, hasMethod := fields["method"]
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	if hasID {
		if _, err := idKey(msg.ID); err != nil {
			return msg, err
		}
	}
	if hasMethod {
		if msg.Method == "" || hasResult || hasError || !validParams(msg.Params) {
			return msg, ErrProtocol
		}
		return msg, nil
	}
	if !hasID || hasResult == hasError {
		return msg, ErrProtocol
	}
	if _, hasParams := fields["params"]; hasParams {
		return msg, ErrProtocol
	}
	if hasError {
		var failure map[string]json.RawMessage
		if json.Unmarshal(fields["error"], &failure) != nil {
			return msg, ErrProtocol
		}
		var code *int
		var text *string
		if json.Unmarshal(failure["code"], &code) != nil || code == nil || json.Unmarshal(failure["message"], &text) != nil || text == nil {
			return msg, ErrProtocol
		}
		msg.Error = &wireError{Code: *code}
	}
	return msg, nil
}

func idKey(id json.RawMessage) (string, error) {
	if len(id) == 0 {
		return "", ErrProtocol
	}
	if id[0] == '"' {
		var value string
		if json.Unmarshal(id, &value) != nil {
			return "", ErrProtocol
		}
		return "s:" + value, nil
	}
	// ACP request identifiers are strings or integers, never null or fractions.
	value, err := strconv.ParseInt(string(id), 10, 64)
	if err != nil {
		return "", ErrProtocol
	}
	return "n:" + strconv.FormatInt(value, 10), nil
}

func validParams(params json.RawMessage) bool {
	if len(params) == 0 {
		return true
	}
	value := bytes.TrimSpace(params)
	return len(value) > 0 && (value[0] == '{' || value[0] == '[') && utf8.Valid(value) && json.Valid(value)
}
