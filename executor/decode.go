package executor

import (
	"encoding/json"
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
	usage *Usage
	parse func(*streamDecoder, json.RawMessage) string
}

func (d *streamDecoder) Decode(line string) string {
	var raw json.RawMessage
	if json.Unmarshal([]byte(line), &raw) != nil {
		return line
	}
	return d.parse(d, raw)
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
		return textFromBlocks(event.Message.Content)
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
			TextDelta string `json:"text_delta"`
		} `json:"step_update"`
		Result struct {
			Usage *struct {
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
		return event.StepUpdate.TextDelta
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
		return ""
	default:
		return ""
	}
}

func decodeCodex(d *streamDecoder, raw json.RawMessage) string {
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
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
		if event.Item.Type != "agent_message" {
			return ""
		}
		return event.Item.Text
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
		Type    string `json:"type"`
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
		return textFromBlocks(event.Message.Content)
	case "result":
		if event.Usage == nil && event.TotalCostUSD == nil {
			return ""
		}
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
		return ""
	default:
		return ""
	}
}

func decodeOpencode(d *streamDecoder, raw json.RawMessage) string {
	var event struct {
		Type string `json:"type"`
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
		return event.Part.Text
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
