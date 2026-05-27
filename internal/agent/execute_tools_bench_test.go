package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yourorg/agent-service/internal/bedrock"
	"github.com/yourorg/agent-service/internal/tools"
)

func BenchmarkExecuteTools(b *testing.B) {
	registry := tools.NewRegistry()
	registry.Register(&tools.Tool{
		Definition: bedrock.Tool{
			Type:     "function",
			Function: bedrock.ToolFunction{Name: "slow_tool", Description: "Slow tool", Parameters: map[string]any{"type": "object"}},
		},
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			time.Sleep(10 * time.Millisecond) // Simulate slow operation
			return "done", nil
		},
	})

	a := newTestAgent(&testing.T{}, &fakeLLM{}, nil, nil, nil, registry)

	toolCalls := make([]bedrock.ToolCall, 10)
	for i := 0; i < 10; i++ {
		toolCalls[i] = bedrock.ToolCall{
			ID: "tc-1",
			Type: "function",
			Function: bedrock.FunctionCall{Name: "slow_tool", Arguments: `{}`},
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		toolsUsed := map[string]struct{}{}
		_ = a.executeTools(context.Background(), "sess-bench", toolCalls, nil, toolsUsed)
	}
}
