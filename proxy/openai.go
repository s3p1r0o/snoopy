package main

// openai.go holds the OpenAI endpoint surface of serve.go: the request/response
// types, their translation to and from the Rill completion contract, the error
// envelope, and the SSE writer.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OpenAI request types. Only the fields translated to the Rill completion API are declared.
// Multi-part content (e.g. images) is rejected because the Rill contract has no image content block.

type chatCompletionRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []openAITool    `json:"tools"`
	Stream   bool            `json:"stream"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// rillMessages translates OpenAI chat messages to Rill completion messages.
func rillMessages(msgs []openAIMessage) ([]Message, error) {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		text, err := messageContent(m.Content)
		if err != nil {
			return nil, err
		}
		if m.Role == "tool" {
			out = append(out, Message{
				Role:   "user",
				Blocks: []Block{{ToolResult: &ToolResult{ID: m.ToolCallID, Content: text}}},
			})
			continue
		}
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return nil, fmt.Errorf("unsupported message role %q", m.Role)
		}
		blocks := make([]Block, 0, len(m.ToolCalls)+1)
		if text != "" {
			blocks = append(blocks, Block{Text: text})
		}
		for _, tc := range m.ToolCalls {
			// OpenAI carries tool call arguments as a JSON string, as does the Rill
			// contract (ToolCall.input is a google.protobuf.Struct); pass the raw JSON through.
			arguments := strings.TrimSpace(tc.Function.Arguments)
			if arguments == "" {
				continue
			}
			if !json.Valid([]byte(arguments)) {
				return nil, fmt.Errorf("invalid arguments for tool call %q", tc.Function.Name)
			}
			blocks = append(blocks, Block{ToolCall: &ToolCall{ID: tc.ID, Name: tc.Function.Name, Input: arguments}})
		}
		out = append(out, Message{Role: m.Role, Blocks: blocks})
	}
	return out, nil
}

// messageContent extracts the text from OpenAI message content, which may be a string, an array of content parts, or null.
func messageContent(content json.RawMessage) (string, error) {
	if len(content) == 0 || string(content) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return "", fmt.Errorf("invalid message content")
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", fmt.Errorf("message content type %q is not supported: the Rill completion API only accepts text, tool calls, and tool results", p.Type)
		}
		sb.WriteString(p.Text)
	}
	return sb.String(), nil
}

// rillTools translates OpenAI tool definitions to Rill tools, ignoring any non-function tools.
func rillTools(tools []openAITool) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		out = append(out, Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: string(t.Function.Parameters),
		})
	}
	return out
}

// OpenAI response types. Tool calls carry arguments as a JSON string, per the OpenAI chat API.

type completionResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []choice       `json:"choices"`
	Usage   *responseUsage `json:"usage,omitempty"`
}

type choice struct {
	Index        int        `json:"index"`
	Message      *choiceMsg `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type choiceMsg struct {
	Role      string             `json:"role"`
	Content   string             `json:"content"`
	ToolCalls []responseToolCall `json:"tool_calls,omitempty"`
}

type responseToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type responseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// completionFromUpstream builds a single (non-streamed) OpenAI completion response from an upstream response.
func completionFromUpstream(model string, resp *CompleteResponse) *completionResponse {
	msg, finish := choiceMessage(resp.Message)
	return &completionResponse{
		ID:      newCompletionID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []choice{{Index: 0, Message: msg, FinishReason: finish}},
		Usage: &responseUsage{
			PromptTokens:     int(resp.InputTokens),
			CompletionTokens: int(resp.OutputTokens),
			TotalTokens:      int(resp.InputTokens) + int(resp.OutputTokens),
		},
	}
}

// choiceMessage converts an upstream completion message to an OpenAI choice message, and derives the finish reason.
func choiceMessage(msg *Message) (*choiceMsg, string) {
	out := &choiceMsg{Role: "assistant"}
	if msg == nil {
		return out, "stop"
	}
	if len(msg.Blocks) == 0 && msg.Data != "" {
		out.Content += msg.Data
	}
	for i, blk := range msg.Blocks {
		switch {
		case blk.ToolCall != nil:
			arguments := strings.TrimSpace(blk.ToolCall.Input)
			if arguments == "" {
				arguments = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, responseToolCall{
				Index: i,
				ID:    blk.ToolCall.ID,
				Type:  "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: blk.ToolCall.Name, Arguments: arguments},
			})
		default:
			out.Content += blk.Text
		}
	}
	finish := "stop"
	if len(out.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	return out, finish
}

// writeSSE streams the upstream response as OpenAI-style Server-Sent Events.
// The upstream contract is unary, so the response is sent as a single full-content chunk; token-by-token streaming is not possible.
func writeSSE(w http.ResponseWriter, model string, resp *CompleteResponse) {
	msg, finish := choiceMessage(resp.Message)
	id := newCompletionID()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	chunk := func(delta map[string]any, reason string) {
		b, _ := json.Marshal(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": reason,
			}},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flush()
	}
	chunk(map[string]any{"role": "assistant"}, "")
	if msg.Content != "" {
		chunk(map[string]any{"content": msg.Content}, "")
	}
	for i := range msg.ToolCalls {
		chunk(map[string]any{"tool_calls": []responseToolCall{msg.ToolCalls[i]}}, "")
	}
	chunk(map[string]any{}, finish)
	fmt.Fprint(w, "data: [DONE]\n\n")
	flush()
}

// writeJSON writes a JSON response, including the OpenAI error envelope if the status is an error.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// openAIError writes an error in the OpenAI error envelope.
func openAIError(w http.ResponseWriter, status int, format string, args ...any) {
	openAIErrorCode(w, status, "", format, args...)
}

// openAIErrorCode writes an error in the OpenAI error envelope with the given type and error code.
func openAIErrorCode(w http.ResponseWriter, status int, code string, format string, args ...any) {
	err := map[string]any{
		"message": fmt.Sprintf(format, args...),
	}
	// Matching the upstream provider's convention: request problems are invalid_request_error, everything else is api_error.
	if status >= 500 {
		err["type"] = "api_error"
	} else {
		err["type"] = "invalid_request_error"
	}
	if code != "" {
		err["code"] = code
	}
	writeJSON(w, status, map[string]any{"error": err})
}

func newCompletionID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
}
