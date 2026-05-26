package config

import (
	"testing"
)

func TestExpandEnv(t *testing.T) {
	t.Setenv("TEST_ENV_VAR_1", "value1")
	t.Setenv("TEST_ENV_VAR_2", "value2")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no env vars",
			input:    "plain string",
			expected: "plain string",
		},
		{
			name:     "single env var",
			input:    "prefix ${TEST_ENV_VAR_1} suffix",
			expected: "prefix value1 suffix",
		},
		{
			name:     "multiple env vars",
			input:    "${TEST_ENV_VAR_1} and ${TEST_ENV_VAR_2}",
			expected: "value1 and value2",
		},
		{
			name:     "missing env var",
			input:    "missing ${TEST_ENV_MISSING} var",
			expected: "missing  var",
		},
		{
			name:     "no brackets",
			input:    "prefix $TEST_ENV_VAR_1 suffix",
			expected: "prefix value1 suffix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandEnv(tt.input)
			if result != tt.expected {
				t.Errorf("expandEnv(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
