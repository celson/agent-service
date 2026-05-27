package config

import (
	"reflect"
	"testing"
)

func TestParseFrontmatterUses(t *testing.T) {
	tests := []struct {
		name     string
		front    string
		expected []string
	}{
		{
			name:     "empty string",
			front:    "",
			expected: nil,
		},
		{
			name: "no uses",
			front: `
name: test
description: foo
`,
			expected: nil,
		},
		{
			name: "single item inline",
			front: `
uses: skill1
`,
			expected: []string{"skill1"},
		},
		{
			name: "multiple items comma-separated",
			front: `
uses: skill1, skill2
`,
			expected: []string{"skill1", "skill2"},
		},
		{
			name: "multiple items with extra spaces",
			front: `
uses:   skill1  ,   skill2
`,
			expected: []string{"skill1", "skill2"},
		},
		{
			name: "items with empty values between commas",
			front: `
uses: skill1, , skill2,
`,
			expected: []string{"skill1", "skill2"},
		},
		{
			name: "yaml list empty value",
			front: `
uses:
  - skill1
  - skill2
`,
			expected: []string{"skill1", "skill2"},
		},
		{
			name: "yaml list with tilde",
			front: `
uses: ~
  - skill1
  - skill2
`,
			expected: []string{"skill1", "skill2"},
		},
		{
			name: "yaml list interrupted by other key",
			front: `
uses:
  - skill1
  - skill2
other_key: value
  - skill3
`,
			expected: []string{"skill1", "skill2"},
		},
		{
			name: "yaml list with blank lines",
			front: `
uses:
  - skill1

  - skill2
`,
			expected: []string{"skill1", "skill2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFrontmatterUses(tt.front)
			if !reflect.DeepEqual(got, tt.expected) {
				// Treat nil slice and empty slice equivalently for this test
				if len(got) == 0 && len(tt.expected) == 0 {
					return
				}
				t.Errorf("parseFrontmatterUses() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func BenchmarkParseFrontmatterUses(b *testing.B) {
	front := `
name: test
description: Test skill
uses: skill1, skill2, skill3, skill4, skill5, skill6, skill7, skill8, skill9, skill10
timeout: 10s
`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseFrontmatterUses(front)
	}
}
