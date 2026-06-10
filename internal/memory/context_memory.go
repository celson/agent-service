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
	// preserveRecentN é o número mínimo de mensagens recentes mantidas
	// intactas após uma compactação.
	preserveRecentN = 4
)

// ContextMemory gerencia a janela de tokens ativa da conversa.
// Não é seguro para uso concorrente: crie uma instância por execução.
type ContextMemory struct {
	system    string
	messages  []bedrock.Message
	maxTokens int
}

func NewContextMemory(maxTokens int) *ContextMemory {
	if maxTokens <= 0 {
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

	split := c.compactionSplit()
	if split <= 0 {
		return nil
	}

	toSummarize := c.messages[:split]
	recent := c.messages[split:]

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

	summary := resp.Choices[0].Message.Text()

	compacted := make([]bedrock.Message, 0, len(recent)+2)
	compacted = append(compacted,
		bedrock.Message{Role: "user", Content: "[Resumo da conversa anterior]\n" + summary},
		bedrock.Message{Role: "assistant", Content: "Entendido. Continuo a partir deste contexto."},
	)
	c.messages = append(compacted, recent...)
	return nil
}

// compactionSplit devolve o índice onde o histórico é cortado: tudo antes é
// sumarizado, tudo a partir dele é preservado. O corte recua enquanto a
// primeira mensagem preservada for um tool result — separá-la do assistant
// turn que contém o tool_use correspondente produziria uma conversa inválida
// para a API Anthropic (tool_result órfão).
func (c *ContextMemory) compactionSplit() int {
	split := len(c.messages) - preserveRecentN
	for split > 0 && c.messages[split].Role == "tool" {
		split--
	}
	return split
}

func (c *ContextMemory) estimateTokens() int {
	total := len(c.system) / 4
	for _, msg := range c.messages {
		total += len(msg.Text()) / 4
		// Tool calls também ocupam janela: nome + argumentos JSON.
		for _, tc := range msg.ToolCalls {
			total += (len(tc.Function.Name) + len(tc.Function.Arguments)) / 4
		}
	}
	return total
}

func formatForSummary(messages []bedrock.Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(msg.Role + ": ")
		sb.WriteString(msg.Text())
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&sb, "[tool_call %s(%s)]", tc.Function.Name, tc.Function.Arguments)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
