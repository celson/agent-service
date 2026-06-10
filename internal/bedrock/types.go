package bedrock

import "strings"

// Os tipos abaixo replicam, com os mesmos shapes, o que existia no pacote
// openrouter. Mantemos formato OpenAI-like no contrato externo (o que o agent,
// memory e tools consomem); a tradução para o formato Anthropic acontece em
// translate.go.

type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // string ou []ContentPart
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// Text extrai o texto plano de Content, que pode chegar como string,
// []ContentPart (shape tipado) ou []any (JSON genérico). Múltiplos blocos de
// texto são concatenados com newline; blocos não-texto são ignorados.
func (m Message) Text() string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []ContentPart:
		var out strings.Builder
		for _, p := range v {
			if p.Type == "text" && p.Text != "" {
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
				out.WriteString(p.Text)
			}
		}
		return out.String()
	case []any:
		var out strings.Builder
		for _, p := range v {
			mp, ok := p.(map[string]any)
			if !ok || mp["type"] != "text" {
				continue
			}
			if t, ok := mp["text"].(string); ok && t != "" {
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
				out.WriteString(t)
			}
		}
		return out.String()
	}
	return ""
}

type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
