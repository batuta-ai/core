package acp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// PermissionRequest is untrusted worker data, bounded by the transport frame
// limit. It is for local policy evaluation only, never a user question or log.
type PermissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string          `json:"toolCallId"`
		Kind       string          `json:"kind"`
		RawInput   json.RawMessage `json:"rawInput"`
		Locations  []struct {
			Path string `json:"path"`
		} `json:"locations"`
	} `json:"toolCall"`
	Options []PermissionOption `json:"options"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

// PermissionPolicy selects an offered option ID using the caller's existing
// authorization. Empty or invalid selections reject. It must return promptly
// and respect ctx; it must not wait for human input or infer consent from prose.
// No selection is cached, including allow_always: each request needs a decision.
type PermissionPolicy func(context.Context, PermissionRequest) string

func (s *Session) permission(ctx context.Context, request Request, prompting bool) error {
	var params PermissionRequest
	valid := request.Method == "session/request_permission" && json.Unmarshal(request.Params, &params) == nil && params.SessionID == s.id && strings.TrimSpace(params.ToolCall.ToolCallID) != "" && len(params.Options) > 0
	ids := make(map[string]bool, len(params.Options))
	for _, option := range params.Options {
		if strings.TrimSpace(option.OptionID) == "" || ids[option.OptionID] {
			valid = false
		}
		ids[option.OptionID] = true
		switch option.Kind {
		case "allow_once", "allow_always", "reject_once", "reject_always":
		default:
			valid = false
		}
	}
	selected := ""
	if valid && prompting && s.config.PermissionPolicy != nil && ctx.Err() == nil && s.conn.Err() == nil {
		// The callback cannot mutate the offered options used to validate its answer.
		options := slices.Clone(params.Options)
		policyCtx, cancel := context.WithTimeout(ctx, s.conn.options.RequestTimeout)
		choice := s.config.PermissionPolicy(policyCtx, params)
		if policyCtx.Err() == nil && s.conn.Err() == nil {
			for _, option := range options {
				if option.OptionID == choice && (option.Kind == "allow_once" || option.Kind == "allow_always") {
					selected = choice
					break
				}
			}
		}
		cancel()
	}
	if selected == "" {
		// Rejection is terminal even if the peer races a success reply or disconnects
		// before accepting the cancellation response.
		responseErr := s.conn.Respond(ctx, request.ID, json.RawMessage(`{"outcome":{"outcome":"cancelled"}}`), nil)
		s.conn.fail(ErrPermissionDenied)
		return errors.Join(ErrPermissionDenied, responseErr)
	}
	response, _ := json.Marshal(struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}{Outcome: struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId"`
	}{"selected", selected}})
	return s.conn.Respond(ctx, request.ID, response, nil)
}
