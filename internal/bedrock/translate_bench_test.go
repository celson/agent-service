package bedrock

import (
	"strings"
	"testing"
)

func BenchmarkMessageText_String(b *testing.B) {
	msg := Message{Content: strings.Repeat("A", 100)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = messageText(msg)
	}
}

func BenchmarkMessageText_ContentParts_Small(b *testing.B) {
	msg := Message{Content: []ContentPart{
		{Type: "text", Text: "Hello, "},
		{Type: "text", Text: "world!"},
	}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = messageText(msg)
	}
}

func BenchmarkMessageText_ContentParts_Large(b *testing.B) {
	parts := make([]ContentPart, 100)
	for i := 0; i < 100; i++ {
		parts[i] = ContentPart{Type: "text", Text: "chunk "}
	}
	msg := Message{Content: parts}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = messageText(msg)
	}
}

func BenchmarkMessageText_Any_Large(b *testing.B) {
	parts := make([]any, 100)
	for i := 0; i < 100; i++ {
		parts[i] = map[string]any{"type": "text", "text": "chunk "}
	}
	msg := Message{Content: parts}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = messageText(msg)
	}
}
