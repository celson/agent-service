package memory

import (
	"testing"
)

func TestSanitizeUTF8(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Valid ASCII",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "Valid UTF-8 with emojis and special chars",
			input:    "olá, mundo 🌎",
			expected: "olá, mundo 🌎",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "Invalid UTF-8 bytes exclusively",
			input:    "\xff\xfe\xfd",
			expected: "",
		},
		{
			name:     "Mixed valid and invalid UTF-8 bytes",
			input:    "hello\xffworld",
			expected: "helloworld",
		},
		{
			name:     "Invalid sequence in the middle",
			input:    "a\xfe\xfe\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xffb",
			expected: "ab",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeUTF8(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeUTF8(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
