package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Decoder turns one CLI stream-json line into the agent text it carries.
// Usage is the session counters from the final event, or the sum of per-step
// counters when a format repeats usage every step.
type Decoder interface {
	Decode(line string) string
	Usage() *Usage
}

type streamDecoder struct {
	usage     *Usage
	parse     func(*streamDecoder, json.RawMessage) string
	lastByte  byte
	hasOutput bool
}

func (d *streamDecoder) Decode(line string) string {
	var raw json.RawMessage
	output := line
	if json.Unmarshal([]byte(line), &raw) == nil {
		output = d.parse(d, raw)
	}
	if output != "" {
		d.lastByte = output[len(output)-1]
		d.hasOutput = true
	}
	return output
}

func (d *streamDecoder) Usage() *Usage {
	return d.usage
}

// LookupDecoder returns a fresh decoder for a recorded CLI format name.
func LookupDecoder(name string) Decoder {
	switch name {
	case "cursor-stream-json":
		return &streamDecoder{parse: decodeCursor}
	case "agy-stream-json":
		return &streamDecoder{parse: decodeAgy}
	case "codex-json":
		return &streamDecoder{parse: decodeCodex}
	case "claude-stream-json":
		return &streamDecoder{parse: decodeClaude}
	case "opencode-json":
		return &streamDecoder{parse: decodeOpencode}
	default:
		return nil
	}
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textFromBlocks(blocks []textBlock) string {
	var text strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func completeMessage(text string) string {
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

func providerLine(message string) string {
	return strings.Join(strings.Fields(message), " ") + "\n"
}

func decodeCursor(d *streamDecoder, raw json.RawMessage) string {
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Content []textBlock `json:"content"`
		} `json:"message"`
		Usage *struct {
			InputTokens      *int64 `json:"inputTokens"`
			OutputTokens     *int64 `json:"outputTokens"`
			CacheReadTokens  *int64 `json:"cacheReadTokens"`
			CacheWriteTokens *int64 `json:"cacheWriteTokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	switch event.Type {
	case "assistant":
		return completeMessage(textFromBlocks(event.Message.Content))
	case "result":
		if event.Usage != nil {
			d.usage = &Usage{
				InputTokens:      event.Usage.InputTokens,
				OutputTokens:     event.Usage.OutputTokens,
				CacheReadTokens:  event.Usage.CacheReadTokens,
				CacheWriteTokens: event.Usage.CacheWriteTokens,
			}
		}
		return ""
	default:
		return ""
	}
}

func decodeAgy(d *streamDecoder, raw json.RawMessage) string {
	var event struct {
		Event      string `json:"event"`
		StepUpdate struct {
			StepType  string `json:"step_type"`
			State     string `json:"state"`
			TextDelta string `json:"text_delta"`
		} `json:"step_update"`
		Result struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Usage  *struct {
				InputTokens     *int64 `json:"input_tokens"`
				OutputTokens    *int64 `json:"output_tokens"`
				ThinkingTokens  *int64 `json:"thinking_tokens"`
				CacheReadTokens *int64 `json:"cache_read_tokens"`
				TotalTokens     *int64 `json:"total_tokens"`
			} `json:"usage"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	switch event.Event {
	case "step_update":
		text := event.StepUpdate.TextDelta
		if event.StepUpdate.StepType == "agent_response" && event.StepUpdate.State == "DONE" && text != "" {
			return completeMessage(text)
		}
		if event.StepUpdate.StepType == "agent_response" && event.StepUpdate.State == "DONE" && d.hasOutput && d.lastByte != '\n' {
			return "\n"
		}
		return text
	case "result":
		if event.Result.Usage != nil {
			d.usage = &Usage{
				InputTokens:         event.Result.Usage.InputTokens,
				OutputTokens:        event.Result.Usage.OutputTokens,
				ReasoningTokens:     event.Result.Usage.ThinkingTokens,
				CacheReadTokens:     event.Result.Usage.CacheReadTokens,
				ReportedTotalTokens: event.Result.Usage.TotalTokens,
			}
		}
		if event.Result.Status == "ERROR" {
			return providerLine("provider error: " + event.Result.Error)
		}
		return ""
	default:
		return ""
	}
}

func decodeCodex(d *streamDecoder, raw json.RawMessage) string {
	var event struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
		Item struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Message string `json:"message"`
		} `json:"item"`
		Usage *struct {
			InputTokens           *int64 `json:"input_tokens"`
			CachedInputTokens     *int64 `json:"cached_input_tokens"`
			CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
			OutputTokens          *int64 `json:"output_tokens"`
			ReasoningOutputTokens *int64 `json:"reasoning_output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	switch event.Type {
	case "item.completed":
		if event.Item.Type == "error" {
			return providerLine("provider notice: " + event.Item.Message)
		}
		if event.Item.Type != "agent_message" {
			return ""
		}
		return completeMessage(event.Item.Text)
	case "error":
		return providerLine("provider error: " + event.Message)
	case "turn.failed":
		return providerLine("provider error: " + event.Error.Message)
	case "turn.completed":
		if event.Usage != nil {
			d.usage = &Usage{
				InputTokens:      event.Usage.InputTokens,
				CacheReadTokens:  event.Usage.CachedInputTokens,
				CacheWriteTokens: event.Usage.CacheWriteInputTokens,
				OutputTokens:     event.Usage.OutputTokens,
				ReasoningTokens:  event.Usage.ReasoningOutputTokens,
				CacheSemantics:   CacheSemanticsSubset,
			}
		}
		return ""
	default:
		return ""
	}
}

func decodeClaude(d *streamDecoder, raw json.RawMessage) string {
	var event struct {
		Type           string `json:"type"`
		IsError        bool   `json:"is_error"`
		APIErrorStatus int    `json:"api_error_status"`
		Result         string `json:"result"`
		RateLimitInfo  struct {
			Status        string `json:"status"`
			RateLimitType string `json:"rateLimitType"`
			ResetsAt      int64  `json:"resetsAt"`
		} `json:"rate_limit_info"`
		Message struct {
			Content []textBlock `json:"content"`
		} `json:"message"`
		TotalCostUSD *float64 `json:"total_cost_usd"`
		Usage        *struct {
			InputTokens              *int64 `json:"input_tokens"`
			OutputTokens             *int64 `json:"output_tokens"`
			CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	switch event.Type {
	case "assistant":
		return completeMessage(textFromBlocks(event.Message.Content))
	case "rate_limit_event":
		if event.RateLimitInfo.Status == "allowed" {
			return ""
		}
		return providerLine(fmt.Sprintf("provider limit: %s %s resetsAt %d", event.RateLimitInfo.RateLimitType, event.RateLimitInfo.Status, event.RateLimitInfo.ResetsAt))
	case "result":
		if event.Usage != nil || event.TotalCostUSD != nil {
			usage := &Usage{CacheSemantics: CacheSemanticsAdditive}
			if event.Usage != nil {
				usage.InputTokens = event.Usage.InputTokens
				usage.OutputTokens = event.Usage.OutputTokens
				usage.CacheWriteTokens = event.Usage.CacheCreationInputTokens
				usage.CacheReadTokens = event.Usage.CacheReadInputTokens
			}
			if event.TotalCostUSD != nil {
				usage.CostAmount = event.TotalCostUSD
				usage.CostCurrency = "USD"
			}
			d.usage = usage
		}
		if event.IsError {
			return providerLine(fmt.Sprintf("provider error: api_error_status %d: %s", event.APIErrorStatus, event.Result))
		}
		return ""
	default:
		return ""
	}
}

func decodeOpencode(d *streamDecoder, raw json.RawMessage) string {
	var event struct {
		Type  string `json:"type"`
		Error struct {
			Name string `json:"name"`
			Data struct {
				StatusCode *int64 `json:"statusCode"`
				Message    string `json:"message"`
			} `json:"data"`
		} `json:"error"`
		Part struct {
			Text   string `json:"text"`
			Tokens *struct {
				Total     *int64 `json:"total"`
				Input     *int64 `json:"input"`
				Output    *int64 `json:"output"`
				Reasoning *int64 `json:"reasoning"`
				Cache     struct {
					Write *int64 `json:"write"`
					Read  *int64 `json:"read"`
				} `json:"cache"`
			} `json:"tokens"`
			Cost *float64 `json:"cost"`
		} `json:"part"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	switch event.Type {
	case "text":
		return completeMessage(event.Part.Text)
	case "error":
		status := ""
		if event.Error.Data.StatusCode != nil {
			status = fmt.Sprintf(" %d", *event.Error.Data.StatusCode)
		}
		return providerLine("provider error: " + event.Error.Name + status + ": " + event.Error.Data.Message)
	case "step_finish":
		if event.Part.Tokens == nil && event.Part.Cost == nil {
			return ""
		}
		usage := &Usage{}
		if event.Part.Tokens != nil {
			usage.InputTokens = event.Part.Tokens.Input
			usage.OutputTokens = event.Part.Tokens.Output
			usage.ReasoningTokens = event.Part.Tokens.Reasoning
			usage.CacheReadTokens = event.Part.Tokens.Cache.Read
			usage.CacheWriteTokens = event.Part.Tokens.Cache.Write
			usage.ReportedTotalTokens = event.Part.Tokens.Total
		}
		if event.Part.Cost != nil {
			usage.CostAmount = event.Part.Cost
			usage.CostCurrency = "USD"
		}
		d.addUsage(usage)
		return ""
	default:
		return ""
	}
}

func (d *streamDecoder) addUsage(next *Usage) {
	if next == nil {
		return
	}
	if d.usage == nil {
		d.usage = next
		return
	}
	d.usage.InputTokens = addInt64(d.usage.InputTokens, next.InputTokens)
	d.usage.OutputTokens = addInt64(d.usage.OutputTokens, next.OutputTokens)
	d.usage.CacheReadTokens = addInt64(d.usage.CacheReadTokens, next.CacheReadTokens)
	d.usage.CacheWriteTokens = addInt64(d.usage.CacheWriteTokens, next.CacheWriteTokens)
	d.usage.ReasoningTokens = addInt64(d.usage.ReasoningTokens, next.ReasoningTokens)
	d.usage.ReportedTotalTokens = addInt64(d.usage.ReportedTotalTokens, next.ReportedTotalTokens)
	d.usage.CostAmount = addFloat64(d.usage.CostAmount, next.CostAmount)
	if next.CostCurrency != "" {
		d.usage.CostCurrency = next.CostCurrency
	}
}

func addInt64(dst, src *int64) *int64 {
	if src == nil {
		return dst
	}
	n := *src
	if dst != nil {
		n += *dst
	}
	return &n
}

func addFloat64(dst, src *float64) *float64 {
	if src == nil {
		return dst
	}
	n := *src
	if dst != nil {
		n += *dst
	}
	return &n
}
