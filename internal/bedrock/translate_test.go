package bedrock

import (
	"encoding/json"
	"testing"
)

func TestToAnthropicBody_SystemExtraction(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "hi"},
		},
	}
	body, err := toAnthropicBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.System != "you are helpful" {
		t.Errorf("system not extracted, got %q", body.System)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("expected 1 message after extracting system, got %d", len(body.Messages))
	}
	if body.Messages[0].Role != "user" {
		t.Errorf("expected user, got %s", body.Messages[0].Role)
	}
}

func TestToAnthropicBody_ToolCallToToolUse(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{
			{Role: "user", Content: "list files"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: FunctionCall{
							Name:      "file_ops",
							Arguments: `{"op":"list","path":"/"}`,
						},
					},
				},
			},
		},
	}
	body, err := toAnthropicBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(body.Messages))
	}
	asst := body.Messages[1]
	if asst.Role != "assistant" {
		t.Fatalf("expected assistant role, got %s", asst.Role)
	}
	if len(asst.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(asst.Content))
	}
	tu, ok := asst.Content[0].(anthropicToolUseBlock)
	if !ok {
		t.Fatalf("expected tool_use block, got %T", asst.Content[0])
	}
	if tu.ID != "call_1" || tu.Name != "file_ops" {
		t.Errorf("wrong tool_use: %+v", tu)
	}
}

func TestToAnthropicBody_ToolResultRouting(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{
			{Role: "tool", Content: "ok", ToolCallID: "call_1"},
		},
	}
	body, err := toAnthropicBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Fatalf("tool message must become user turn; got %+v", body.Messages)
	}
	tr, ok := body.Messages[0].Content[0].(anthropicToolResultBlock)
	if !ok {
		t.Fatalf("expected tool_result block, got %T", body.Messages[0].Content[0])
	}
	if tr.ToolUseID != "call_1" || tr.Content != "ok" {
		t.Errorf("wrong tool_result: %+v", tr)
	}
}

func TestToAnthropicBody_ToolsConversion(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:        "run_code",
				Description: "exec",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"lang": map[string]any{"type": "string"},
					},
				},
			},
		}},
	}
	body, err := toAnthropicBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(body.Tools))
	}
	if body.Tools[0].Name != "run_code" {
		t.Errorf("wrong name: %s", body.Tools[0].Name)
	}
	if _, ok := body.Tools[0].InputSchema["properties"]; !ok {
		t.Errorf("input_schema lost properties: %+v", body.Tools[0].InputSchema)
	}
}

func TestToAnthropicBody_EmptyArgumentsDefaultsToEmptyObject(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{ID: "x", Type: "function", Function: FunctionCall{Name: "noop"}},
				},
			},
		},
	}
	body, err := toAnthropicBody(req)
	if err != nil {
		t.Fatal(err)
	}
	tu := body.Messages[0].Content[0].(anthropicToolUseBlock)
	if string(tu.Input) != "{}" {
		t.Errorf("expected default {}, got %s", string(tu.Input))
	}
}

func TestFromAnthropicResponse_TextOnly(t *testing.T) {
	resp := anthropicResponse{
		ID:    "msg_1",
		Model: "claude",
		Content: []anthropicContentBlock{
			{Type: "text", Text: "hello"},
		},
		StopReason: "end_turn",
		Usage:      &anthropicUsage{InputTokens: 10, OutputTokens: 5},
	}
	out := fromAnthropicResponse(resp)
	if len(out.Choices) != 1 {
		t.Fatal("missing choice")
	}
	if out.Choices[0].Message.Content != "hello" {
		t.Errorf("wrong text: %v", out.Choices[0].Message.Content)
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Errorf("end_turn must map to stop, got %s", out.Choices[0].FinishReason)
	}
	if out.Usage.TotalTokens != 15 {
		t.Errorf("usage total wrong: %d", out.Usage.TotalTokens)
	}
}

func TestFromAnthropicResponse_ToolUse(t *testing.T) {
	resp := anthropicResponse{
		Content: []anthropicContentBlock{
			{Type: "tool_use", ID: "toolu_1", Name: "file_ops", Input: json.RawMessage(`{"op":"read"}`)},
		},
		StopReason: "tool_use",
	}
	out := fromAnthropicResponse(resp)
	choice := out.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Errorf("tool_use must map to tool_calls, got %s", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(choice.Message.ToolCalls))
	}
	tc := choice.Message.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "file_ops" {
		t.Errorf("wrong tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"op":"read"}` {
		t.Errorf("arguments not preserved: %s", tc.Function.Arguments)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn":      "stop",
		"stop_sequence": "stop",
		"":              "stop",
		"tool_use":      "tool_calls",
		"max_tokens":    "length",
		"weird":         "weird",
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMessageText_AllShapes(t *testing.T) {
	if messageText(Message{Content: "hi"}) != "hi" {
		t.Error("string shape failed")
	}
	if messageText(Message{Content: []ContentPart{{Type: "text", Text: "a"}, {Type: "text", Text: "b"}}}) != "ab" {
		t.Error("[]ContentPart shape failed")
	}
	if messageText(Message{Content: []any{map[string]any{"type": "text", "text": "c"}}}) != "c" {
		t.Error("[]any shape failed")
	}
}
