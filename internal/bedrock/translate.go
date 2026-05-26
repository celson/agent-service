package bedrock

import (
	"encoding/json"
	"fmt"
)

// ── Anthropic-on-Bedrock wire types ──────────────────────────────────────────

type anthropicBody struct {
	AnthropicVersion string             `json:"anthropic_version"`
	MaxTokens        int                `json:"max_tokens"`
	Temperature      *float64           `json:"temperature,omitempty"`
	System           string             `json:"system,omitempty"`
	Messages         []anthropicMessage `json:"messages"`
	Tools            []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type anthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      *anthropicUsage         `json:"usage,omitempty"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ── Request: OpenAI-like → Anthropic body ────────────────────────────────────

// toAnthropicBody converte um ChatRequest (formato OpenAI-like) no body JSON
// esperado pelo Bedrock InvokeModel para modelos Anthropic.
func toAnthropicBody(req ChatRequest) (anthropicBody, error) {
	body := anthropicBody{
		AnthropicVersion: anthropicVersion,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 4096
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			body.accumulateSystem(m)
		case "tool":
			body.addToolResult(m)
		case "assistant":
			body.addAssistant(m)
		case "user":
			body.addUser(m)
		default:
			return anthropicBody{}, fmt.Errorf("bedrock: unsupported role %q", m.Role)
		}
	}

	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	return body, nil
}

// accumulateSystem concatena texto da mensagem system no campo top-level
// body.System (Anthropic não aceita system como entry em messages).
func (b *anthropicBody) accumulateSystem(m Message) {
	s := messageText(m)
	if s == "" {
		return
	}
	if b.System != "" {
		b.System += "\n\n"
	}
	b.System += s
}

// addToolResult converte uma mensagem role:"tool" (formato OpenAI) num user
// turn contendo um content block tool_result, referenciando o tool_use_id.
func (b *anthropicBody) addToolResult(m Message) {
	b.Messages = append(b.Messages, anthropicMessage{
		Role: "user",
		Content: []any{anthropicToolResultBlock{
			Type:      "tool_result",
			ToolUseID: m.ToolCallID,
			Content:   messageText(m),
		}},
	})
}

// addAssistant adiciona um assistant turn com text e/ou tool_use blocks. Pula
// silenciosamente se a mensagem não tem nem texto nem tool_calls.
func (b *anthropicBody) addAssistant(m Message) {
	content := []any{}
	if s := messageText(m); s != "" {
		content = append(content, anthropicTextBlock{Type: "text", Text: s})
	}
	for _, tc := range m.ToolCalls {
		raw := json.RawMessage(tc.Function.Arguments)
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		content = append(content, anthropicToolUseBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: raw,
		})
	}
	if len(content) == 0 {
		return
	}
	b.Messages = append(b.Messages, anthropicMessage{Role: "assistant", Content: content})
}

// addUser adiciona um user turn com um text block; pula mensagens vazias.
func (b *anthropicBody) addUser(m Message) {
	s := messageText(m)
	if s == "" {
		return
	}
	b.Messages = append(b.Messages, anthropicMessage{
		Role:    "user",
		Content: []any{anthropicTextBlock{Type: "text", Text: s}},
	})
}

// ── Response: Anthropic → OpenAI-like Choice ─────────────────────────────────

func fromAnthropicResponse(resp anthropicResponse) ChatResponse {
	msg := Message{Role: "assistant"}

	var textParts string
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			if textParts != "" {
				textParts += "\n"
			}
			textParts += b.Text
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:   b.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}
	msg.Content = textParts

	out := ChatResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: mapStopReason(resp.StopReason),
		}},
	}
	if resp.Usage != nil {
		out.Usage = Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
	}
	return out
}

// mapStopReason converte stop_reason do Anthropic para finish_reason do
// formato OpenAI (que o agent loop espera).
func mapStopReason(s string) string {
	switch s {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "end_turn", "stop_sequence", "":
		return "stop"
	default:
		return s
	}
}

// messageText extrai o texto plano de um Message.Content (string ou
// []ContentPart ou []any).
func messageText(m Message) string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []ContentPart:
		var out string
		for _, p := range v {
			if p.Type == "text" {
				out += p.Text
			}
		}
		return out
	case []any:
		var out string
		for _, p := range v {
			if mp, ok := p.(map[string]any); ok && mp["type"] == "text" {
				if t, ok := mp["text"].(string); ok {
					out += t
				}
			}
		}
		return out
	}
	return ""
}
