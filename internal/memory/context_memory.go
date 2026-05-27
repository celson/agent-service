package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/yourorg/agent-service/internal/bedrock"
)

const (
	compactionThreshold = 0.80
	defaultMaxTokens    = 180_000
)

// ContextMemory gerencia a janela de tokens ativa da conversa.
type ContextMemory struct {
	system    string
	messages  []bedrock.Message
	maxTokens int
}

func NewContextMemory(maxTokens int) *ContextMemory {
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	return &ContextMemory{maxTokens: maxTokens}
}

func (c *ContextMemory) Reset()             { c.messages = nil }
func (c *ContextMemory) SetSystem(p string) { c.system = p }
func (c *ContextMemory) System() string     { return c.system }
func (c *ContextMemory) Messages() []bedrock.Message {
	// Injeta system como primeira mensagem (formato OpenAI-like). O cliente
	// bedrock extrai esse role:"system" para o campo top-level do body.
	msgs := make([]bedrock.Message, 0, len(c.messages)+1)
	if c.system != "" {
		msgs = append(msgs, bedrock.Message{Role: "system", Content: c.system})
	}
	return append(msgs, c.messages...)
}

func (c *ContextMemory) Add(role, content string) {
	c.messages = append(c.messages, bedrock.Message{Role: role, Content: content})
}

func (c *ContextMemory) AddAssistantMessage(msg bedrock.Message) {
	c.messages = append(c.messages, msg)
}

func (c *ContextMemory) AddToolResults(results []bedrock.Message) {
	c.messages = append(c.messages, results...)
}

// ChatClient é a fatia mínima de *bedrock.Client da qual a compactação depende.
// Definir como interface aqui permite injetar fakes em testes do pacote agent
// sem expor o cliente HTTP real.
type ChatClient interface {
	Chat(ctx context.Context, req bedrock.ChatRequest) (*bedrock.ChatResponse, error)
}

// CompactIfNeeded sumariza o histórico quando a janela estiver quase cheia.
func (c *ContextMemory) CompactIfNeeded(ctx context.Context, llm ChatClient) error {
	estimated := c.estimateTokens()
	if estimated < int(float64(c.maxTokens)*compactionThreshold) {
		return nil
	}

	preserveN := 4
	if len(c.messages) <= preserveN {
		return nil
	}

	toSummarize := c.messages[:len(c.messages)-preserveN]
	recent := c.messages[len(c.messages)-preserveN:]

	resp, err := llm.Chat(ctx, bedrock.ChatRequest{
		Model:     bedrock.DefaultHaikuModel, // modelo leve para sumarizar
		MaxTokens: 1024,
		Messages: []bedrock.Message{
			{Role: "system", Content: "Resuma a conversa de forma concisa, preservando decisões e resultados importantes."},
			{Role: "user", Content: formatForSummary(toSummarize)},
		},
	})
	if err != nil {
		return fmt.Errorf("context compaction failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return fmt.Errorf("context compaction: empty response")
	}

	summary := extractContentString(resp.Choices[0].Message)

	c.messages = []bedrock.Message{
		{Role: "user", Content: "[Resumo da conversa anterior]\n" + summary},
		{Role: "assistant", Content: "Entendido. Continuo a partir deste contexto."},
	}
	c.messages = append(c.messages, recent...)
	return nil
}

func (c *ContextMemory) estimateTokens() int {
	total := len(c.system) / 4
	for _, msg := range c.messages {
		switch v := msg.Content.(type) {
		case string:
			total += len(v) / 4
		case []bedrock.ContentPart:
			for _, p := range v {
				total += len(p.Text) / 4
			}
		}
	}
	return total
}

func formatForSummary(messages []bedrock.Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(msg.Role + ": ")
		sb.WriteString(extractContentString(msg))
		sb.WriteString("\n")
	}
	return sb.String()
}

func extractContentString(msg bedrock.Message) string {
	switch v := msg.Content.(type) {
	case string:
		return v
	case []bedrock.ContentPart:
		for _, part := range v {
			if part.Type == "text" {
				return part.Text
			}
		}
	case []any:
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok {
						return t
					}
				}
			}
		}
	}
	return ""
}
