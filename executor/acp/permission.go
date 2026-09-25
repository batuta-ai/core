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
	denialAssignment = regexp.MustCompile(`(?i)(?:--)?[a-z_][a-z0-9_-]*=`)
	denialFlag       = regexp.MustCompile(`(?i)--[a-z_][a-z0-9_-]*[ \t]+`)
	denialBearer     = regexp.MustCompile(`(?i)\bBearer[ \t]+`)
	denialPrefix     = regexp.MustCompile(`(?i)\b(?:sk-|ghp_|gho_|github_pat_|xox|AKIA)`)
)

func secretKey(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"token", "secret", "password", "passwd", "api_key", "api-key", "apikey", "auth", "credential"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

// redactDenialField keeps the text up to a secret-shaped key or prefix and
// drops the rest of the field: shell words join quoted, unquoted and escaped
// segments in too many ways to find where a secret value ends.
func redactDenialField(value string) string {
	cut := len(value)
	keep := len(value)
	for _, pattern := range []*regexp.Regexp{denialAssignment, denialFlag} {
		for _, match := range pattern.FindAllStringIndex(value, -1) {
			key := strings.TrimLeft(strings.TrimRight(value[match[0]:match[1]], "= \t"), "-")
			if secretKey(key) && match[0] < cut {
				cut, keep = match[0], match[1]
				break
			}
		}
	}
	if match := denialBearer.FindStringIndex(value); match != nil && match[0] < cut {
		cut, keep = match[0], match[1]
	}
	if match := denialPrefix.FindStringIndex(value); match != nil && match[0] < cut {
		cut, keep = match[0], match[0]
	}
	if cut == len(value) {
		return value
	}
	return value[:keep] + "[redacted]"
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
