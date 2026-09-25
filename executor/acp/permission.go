package acp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
)

const maxDeniedPermissions = 4

var (
	denialAssignment = regexp.MustCompile(`(?i)(?:--)?[a-z_][a-z0-9_-]*=(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\S+)`)
	denialFlag       = regexp.MustCompile(`(?i)--[a-z_][a-z0-9_-]*[ \t]+(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\S+)`)
	denialBearer     = regexp.MustCompile(`(?i)\bBearer[ \t]+(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\S+)`)
	denialPrefix     = regexp.MustCompile("(?i)\\b(?:sk-|ghp_|gho_|github_pat_|xox|AKIA)[^\\s\\\"']*")
)

func secretKey(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"token", "secret", "password", "passwd", "api_key", "apikey", "auth", "credential"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

func redactDenialField(value string) string {
	value = denialAssignment.ReplaceAllStringFunc(value, func(match string) string {
		index := strings.IndexByte(match, '=')
		if !secretKey(strings.TrimPrefix(match[:index], "--")) {
			return match
		}
		return match[:index+1] + "[redacted]"
	})
	value = denialFlag.ReplaceAllStringFunc(value, func(match string) string {
		index := strings.IndexAny(match, " \t")
		if !secretKey(match[2:index]) {
			return match
		}
		end := index
		for end < len(match) && (match[end] == ' ' || match[end] == '\t') {
			end++
		}
		return match[:end] + "[redacted]"
	})
	value = denialBearer.ReplaceAllStringFunc(value, func(match string) string {
		index := strings.IndexAny(match, " \t")
		return match[:index] + " [redacted]"
	})
	return denialPrefix.ReplaceAllString(value, "[redacted]")
}

func boundedDenialField(value string, limit int) string {
	value = redactDenialField(value)
	var result strings.Builder
	for _, r := range value {
		candidate := result.String() + string(r)
		encoded, _ := json.Marshal(candidate)
		if len(candidate) > limit || len(encoded)-2 > limit {
			break
		}
		result.WriteRune(r)
	}
	return result.String()
}

// PermissionRequest is untrusted worker data, bounded by the transport frame
// limit. It is for local policy evaluation only, never a user question or log.
type PermissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string          `json:"toolCallId"`
		Kind       string          `json:"kind"`
		Title      string          `json:"title"`
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
	options := slices.Clone(params.Options)
	if valid && prompting && s.config.PermissionPolicy != nil && ctx.Err() == nil && s.conn.Err() == nil {
		// The callback cannot mutate the offered options used to validate its answer.
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
		s.recordDenial(params)
		if valid && prompting {
			for _, option := range options {
				if option.Kind == "reject_once" {
					selected = option.OptionID
					break
				}
			}
		}
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

func (s *Session) recordDenial(params PermissionRequest) {
	s.denialsTotal++
	if len(s.denials) == maxDeniedPermissions {
		return
	}
	var input struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(params.ToolCall.RawInput, &input)
	denial := DeniedPermission{
		Kind:    boundedDenialField(params.ToolCall.Kind, 80),
		Title:   boundedDenialField(params.ToolCall.Title, 80),
		Command: boundedDenialField(input.Command, 160),
	}
	for _, location := range params.ToolCall.Locations[:min(len(params.ToolCall.Locations), 2)] {
		denial.Locations = append(denial.Locations, boundedDenialField(location.Path, 160))
	}
	s.denials = append(s.denials, denial)
}
