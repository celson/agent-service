package agent

import (
	"github.com/yourorg/agent-service/internal/memory"
	"testing"
)

func BenchmarkBuildPromptWithContext(b *testing.B) {
	base := "Base prompt content."
	memories := []string{
		"Memory 1 content here.",
		"Memory 2 content here.",
		"Memory 3 content here.",
		"Memory 4 content here.",
		"Memory 5 content here.",
	}
	past := []memory.Episode{
		{Goal: "Goal 1", Outcome: "Success", Summary: "Summary 1"},
		{Goal: "Goal 2", Outcome: "Success", Summary: "Summary 2"},
		{Goal: "Goal 3", Outcome: "Failure", Summary: "Summary 3"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildPromptWithContext(base, memories, past)
	}
}
