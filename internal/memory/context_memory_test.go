package memory

import (
	"testing"

	"github.com/yourorg/agent-service/internal/bedrock"
)

// TestCompactionSplit_DoesNotOrphanToolResults garante que a compactação nunca
// corta o histórico no meio de um par tool_use/tool_result — um tool_result
// sem o assistant turn anterior é rejeitado pela API Anthropic.
func TestCompactionSplit_DoesNotOrphanToolResults(t *testing.T) {
	c := NewContextMemory(1000)
	c.Add("user", "goal")
	c.AddAssistantMessage(bedrock.Message{Role: "assistant", Content: "thinking"})
	c.AddAssistantMessage(bedrock.Message{
		Role: "assistant",
		ToolCalls: []bedrock.ToolCall{
			{ID: "t1", Type: "function", Function: bedrock.FunctionCall{Name: "a", Arguments: "{}"}},
			{ID: "t2", Type: "function", Function: bedrock.FunctionCall{Name: "b", Arguments: "{}"}},
		},
	})
	c.AddToolResults([]bedrock.Message{
		{Role: "tool", ToolCallID: "t1", Content: "r1"},
		{Role: "tool", ToolCallID: "t2", Content: "r2"},
	})
	c.AddAssistantMessage(bedrock.Message{Role: "assistant", Content: "done"})
	// len=6; corte ingênuo seria 6-4=2, exatamente em cima do primeiro tool
	// result? Não: índice 2 é o assistant com tool_calls. Força o pior caso
	// adicionando mais uma mensagem para o corte cair no tool result.
	c.Add("user", "next")
	// len=7, corte ingênuo = 3 → messages[3] é tool result. Deve recuar para 2.
	split := c.compactionSplit()
	if split < 0 || split >= len(c.messages) {
		t.Fatalf("split out of range: %d", split)
	}
	if c.messages[split].Role == "tool" {
		t.Errorf("split=%d starts recent window at an orphan tool result", split)
	}
}

func TestCompactionSplit_AllToolResultsReturnsZero(t *testing.T) {
	c := NewContextMemory(1000)
	for i := 0; i < 6; i++ {
		c.AddToolResults([]bedrock.Message{{Role: "tool", ToolCallID: "t", Content: "r"}})
	}
	if split := c.compactionSplit(); split != 0 {
		t.Errorf("expected split=0 when history is all tool results, got %d", split)
	}
}

func TestFormatForSummary(t *testing.T) {
	tests := []struct {
		name     string
		messages []bedrock.Message
		expected string
	}{
		{
			name:     "empty messages",
			messages: []bedrock.Message{},
			expected: "",
		},
		{
			name: "single string message",
			messages: []bedrock.Message{
				{Role: "user", Content: "Hello world"},
			},
			expected: "user: Hello world\n",
		},
		{
			name: "multiple string messages",
			messages: []bedrock.Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there"},
			},
			expected: "user: Hello\nassistant: Hi there\n",
		},
		{
			name: "message with []any content structure",
			messages: []bedrock.Message{
				{
					Role: "user",
					Content: []any{
						map[string]any{
							"type": "text",
							"text": "Extracted text content",
						},
						map[string]any{
							"type": "image",
							"url":  "http://example.com/image.png",
						},
					},
				},
			},
			expected: "user: Extracted text content\n",
		},
		{
			name: "message with unsupported content format",
			messages: []bedrock.Message{
				{Role: "system", Content: 12345}, // tipo não suportado
			},
			expected: "system: \n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatForSummary(tt.messages)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
