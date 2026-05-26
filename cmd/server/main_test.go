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

func BenchmarkBuildSREGoal(b *testing.B) {
	alert := alertItem{
		Status: "firing",
		Labels: map[string]string{
			"alertname": "HighCPU",
			"severity":  "critical",
			"service":   "web-server",
			"region":    "us-east-1",
			"instance":  "i-0abcdef1234567890",
			"env":       "production",
			"team":      "sre",
			"foo": "bar",
			"baz": "qux",
			"quux": "corge",
			"grault": "garply",
			"waldo": "fred",
			"plugh": "xyzzy",
			"thud": "foo2",
		},
		Annotations: map[string]string{
			"summary":     "High CPU usage detected",
			"description": "CPU usage is above 90% for the last 5 minutes.",
		},
		StartsAt:    "2023-10-27T10:00:00Z",
		EndsAt:      "2023-10-27T10:05:00Z",
		Fingerprint: "abcdef1234567890",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildSREGoal(alert, "")
	}
}
