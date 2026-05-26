package main

import (
	"testing"
)

func TestHasEmptyRequiredEnv(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		expected bool
	}{
		{
			name: "All values present",
			env: map[string]string{
				"VAR1": "value1",
				"VAR2": "value2",
			},
			expected: false,
		},
		{
			name: "One value is empty",
			env: map[string]string{
				"VAR1": "value1",
				"VAR2": "",
			},
			expected: true,
		},
		{
			name: "All values are empty",
			env: map[string]string{
				"VAR1": "",
				"VAR2": "",
			},
			expected: true,
		},
		{
			name:     "Empty map",
			env:      map[string]string{},
			expected: false,
		},
		{
			name:     "Nil map",
			env:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasEmptyRequiredEnv(tt.env)
			if result != tt.expected {
				t.Errorf("hasEmptyRequiredEnv() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
