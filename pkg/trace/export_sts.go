package trace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/tamnd/tomo/pkg/provider"
)

type stsEnvelope struct {
	Type    string     `json:"type"`
	Message stsMessage `json:"message"`
}

type stsMessage struct {
	Role             string        `json:"role"`
	Content          string        `json:"content"`
	ReasoningContent string        `json:"reasoningContent,omitempty"`
	ToolCalls        []stsToolCall `json:"toolCalls,omitempty"`
	ToolCallID       string        `json:"toolCallId,omitempty"`
	Timestamp        int64         `json:"timestamp,omitempty"`
	Model            string        `json:"model,omitempty"`
}

type stsToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type stsCallMetrics struct {
	Sequence                 int64   `json:"sequence"`
	StartedAt                string  `json:"started_at"`
	DurationMS               int64   `json:"duration_ms"`
	StopReason               string  `json:"stop_reason"`
	Failed                   bool    `json:"failed"`
	InputTokens              int     `json:"input_tokens"`
	CachedInputTokens        int     `json:"cached_input_tokens"`
	CacheWriteInputTokens    int     `json:"cache_write_input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	ReasoningTokens          int     `json:"reasoning_tokens"`
	TotalTokens              int     `json:"total_tokens"`
	PricingKnown             bool    `json:"pricing_known"`
	Pricing                  Pricing `json:"pricing_usd_per_million"`
	InputCostNanos           int64   `json:"input_cost_nanos"`
	CachedInputCostNanos     int64   `json:"cached_input_cost_nanos"`
	CacheWriteInputCostNanos int64   `json:"cache_write_input_cost_nanos"`
	OutputCostNanos          int64   `json:"output_cost_nanos"`
	CostNanos                int64   `json:"cost_nanos"`
	CostUSD                  float64 `json:"cost_usd"`
}

type stsHeader struct {
	Type          string           `json:"type"`
	Harness       string           `json:"harness"`
	ID            string           `json:"id"`
	Name          string           `json:"name,omitempty"`
	Format        string           `json:"format"`
	Provider      string           `json:"provider"`
	Model         string           `json:"model"`
	TaskID        string           `json:"task_id,omitempty"`
	StartedAt     string           `json:"started_at"`
	EndedAt       string           `json:"ended_at,omitempty"`
	Status        string           `json:"status"`
	Usage         Run              `json:"usage"`
	Calls         []stsCallMetrics `json:"calls"`
	Tools         []stableTool     `json:"tools,omitempty"`
	Specification string           `json:"specification"`
}

// ExportSTS writes Hugging Face Session Trace Simple Format JSONL. The first
// line is the session header and every later line is one logical message.
func ExportSTS(dir, runID, output string) error {
	run, err := loadRun(dir, runID)
	if err != nil {
		return err
	}
	calls, err := loadCalls(dir, runID)
	if err != nil {
		return err
	}
	header := stsHeader{
		Type: "session", Harness: "tomo", ID: run.ID, Name: run.TaskLabel,
		Format: "sts", Provider: run.Provider, Model: run.Model, TaskID: run.TaskID,
		StartedAt: run.StartedAt, EndedAt: run.EndedAt, Status: run.Status, Usage: run,
		Specification: "https://huggingface.co/docs/hub/en/session-traces-format",
	}
	if len(calls) > 0 {
		header.Tools = calls[0].Tools
	}
	for _, call := range calls {
		header.Calls = append(header.Calls, stsCallMetrics{
			Sequence: call.Sequence, StartedAt: call.StartedAt, DurationMS: call.DurationMS,
			StopReason: call.StopReason, Failed: len(call.Error) != 0,
			InputTokens: call.InputTokens, CachedInputTokens: call.CachedInputTokens,
			CacheWriteInputTokens: call.CacheWriteInputTokens, OutputTokens: call.OutputTokens,
			ReasoningTokens: call.ReasoningTokens, TotalTokens: call.TotalTokens,
			PricingKnown: call.PricingKnown, Pricing: call.Pricing,
			InputCostNanos: call.InputCostNanos, CachedInputCostNanos: call.CachedCostNanos,
			CacheWriteInputCostNanos: call.CacheWriteCostNanos,
			OutputCostNanos:          call.OutputCostNanos, CostNanos: call.CostNanos, CostUSD: call.CostUSD,
		})
	}

	var payload bytes.Buffer
	enc := json.NewEncoder(&payload)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(header); err != nil {
		return err
	}
	if len(calls) == 0 {
		return writeExport(output, payload.Bytes())
	}
	if calls[0].System != "" {
		if err := encodeSTS(enc, stsMessage{Role: "system", Content: calls[0].System, Timestamp: epochMillis(run.StartedAt)}); err != nil {
			return err
		}
	}
	var previous []provider.Message
	var previousResponse []provider.Block
	for _, call := range calls {
		start := messageOverlap(previous, call.Messages)
		newMessages := call.Messages[start:]
		if len(newMessages) > 0 && newMessages[0].Role == provider.RoleAssistant &&
			reflect.DeepEqual(newMessages[0].Blocks, previousResponse) {
			newMessages = newMessages[1:]
		}
		for _, message := range newMessages {
			if err := encodeProviderMessage(enc, message, call.StartedAt, callModel(run.Model, message.Role)); err != nil {
				return err
			}
		}
		if call.Response != nil {
			at, err := time.Parse(time.RFC3339Nano, call.StartedAt)
			if err != nil {
				return err
			}
			at = at.Add(time.Duration(call.DurationMS) * time.Millisecond)
			if err := encodeBlocks(enc, "assistant", call.Response.Blocks, call.Response.Reasoning, at.UnixMilli(), run.Model); err != nil {
				return err
			}
			previousResponse = call.Response.Blocks
		} else {
			previousResponse = nil
		}
		previous = call.Messages
	}
	return writeExport(output, payload.Bytes())
}

func messageOverlap(previous, current []provider.Message) int {
	limit := len(previous)
	if len(current) < limit {
		limit = len(current)
	}
	for size := limit; size > 0; size-- {
		if reflect.DeepEqual(previous[len(previous)-size:], current[:size]) {
			return size
		}
	}
	return 0
}

func encodeProviderMessage(enc *json.Encoder, message provider.Message, startedAt, model string) error {
	return encodeBlocks(enc, string(message.Role), message.Blocks, "", epochMillis(startedAt), model)
}

func encodeBlocks(enc *json.Encoder, role string, blocks []provider.Block, reasoning string, timestamp int64, model string) error {
	var content strings.Builder
	var toolCalls []stsToolCall
	for _, block := range blocks {
		switch block.Type {
		case provider.BlockText:
			content.WriteString(block.Text)
		case provider.BlockImage:
			if content.Len() > 0 {
				content.WriteByte('\n')
			}
			fmt.Fprintf(&content, "data:%s;base64,%s", block.MediaType, block.Data)
		case provider.BlockToolUse:
			call := stsToolCall{ID: block.ID}
			call.Function.Name = block.Name
			call.Function.Arguments = string(block.Input)
			if call.Function.Arguments == "" {
				call.Function.Arguments = "{}"
			}
			toolCalls = append(toolCalls, call)
		case provider.BlockToolResult:
			value := block.Content
			if block.IsError {
				value = "ERROR: " + value
			}
			if err := encodeSTS(enc, stsMessage{Role: "tool", ToolCallID: block.ToolID, Content: value, Timestamp: timestamp}); err != nil {
				return err
			}
		}
	}
	if content.Len() == 0 && len(toolCalls) == 0 {
		return nil
	}
	return encodeSTS(enc, stsMessage{Role: role, Content: content.String(), ReasoningContent: reasoning, ToolCalls: toolCalls, Timestamp: timestamp, Model: model})
}

func encodeSTS(enc *json.Encoder, message stsMessage) error {
	return enc.Encode(stsEnvelope{Type: "message", Message: message})
}

func callModel(model string, role provider.Role) string {
	if role == provider.RoleAssistant {
		return model
	}
	return ""
}

func epochMillis(value string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}
