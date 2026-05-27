package memory

import (
	"testing"

	"github.com/yourorg/agent-service/internal/bedrock"
)

func TestExtractContentString(t *testing.T) {
	tests := []struct {
		name     string
		msg      bedrock.Message
		expected string
	}{
		{
			name: "Simple String Content",
			msg: bedrock.Message{
				Content: "Hello World",
			},
			expected: "Hello World",
		},
		{
			// Regressão do PR #29: ContentPart tipado (não []any) precisa ser reconhecido.
			name: "ContentPart slice returns first text",
			msg: bedrock.Message{
				Content: []bedrock.ContentPart{
					{Type: "text", Text: "first"},
					{Type: "text", Text: "second"},
				},
			},
			expected: "first",
		},
		{
			name: "Array of Any with Text Type",
			msg: bedrock.Message{
				Content: []any{
					map[string]any{
						"type": "text",
						"text": "Hello from map",
					},
				},
			},
			expected: "Hello from map",
		},
		{
			name: "Array of Any with Non-Text Type",
			msg: bedrock.Message{
				Content: []any{
					map[string]any{
						"type": "image",
						"url":  "http://example.com/image.png",
					},
				},
			},
			expected: "",
		},
		{
			name: "Array of Any with Mixed Types, returns first text",
			msg: bedrock.Message{
				Content: []any{
					map[string]any{
						"type": "image",
						"url":  "http://example.com/image.png",
					},
					map[string]any{
						"type": "text",
						"text": "First text",
					},
					map[string]any{
						"type": "text",
						"text": "Second text",
					},
				},
			},
			expected: "First text",
		},
		{
			name: "Unexpected Content Type",
			msg: bedrock.Message{
				Content: 12345,
			},
			expected: "",
		},
		{
			name: "Nil Content",
			msg: bedrock.Message{
				Content: nil,
			},
			expected: "",
		},
		{
			name: "Array of Any missing type field",
			msg: bedrock.Message{
				Content: []any{
					map[string]any{
						"text": "No type field",
					},
				},
			},
			expected: "",
		},
		{
			name: "Array of Any wrong text field type",
			msg: bedrock.Message{
				Content: []any{
					map[string]any{
						"type": "text",
						"text": 123,
					},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractContentString(tt.msg)
			if result != tt.expected {
				t.Errorf("extractContentString() = %v, want %v", result, tt.expected)
			}
		})
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
