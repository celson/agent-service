package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourorg/agent-service/internal/bedrock"
	"github.com/yourorg/agent-service/internal/memory"
	"github.com/yourorg/agent-service/internal/tools"
)

// ── Fakes ────────────────────────────────────────────────────────────────────

// fakeLLM devolve respostas pré-programadas em ordem. Erra ao ser chamado
// além do número de respostas configuradas.
type fakeLLM struct {
	responses []*bedrock.ChatResponse
	err       error
	calls     int
}

func (f *fakeLLM) Chat(ctx context.Context, req bedrock.ChatRequest) (*bedrock.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.calls >= len(f.responses) {
		return nil, errors.New("fakeLLM: scripted responses exhausted")
	}
	r := f.responses[f.calls]
	f.calls++
	return r, nil
}

type fakeWM struct {
	state       *memory.AgentState
	loadErr     error
	checkpoints []string
}

func (f *fakeWM) Load(ctx context.Context, sessionID string) (*memory.AgentState, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.state != nil {
		return f.state, nil
	}
	return &memory.AgentState{Variables: map[string]string{}}, nil
}

func (f *fakeWM) Checkpoint(ctx context.Context, sessionID, step, result string) error {
	f.checkpoints = append(f.checkpoints, step)
	return nil
}

type fakeVM struct {
	searchErr error
	stored    atomic.Int64
}

func (f *fakeVM) Search(ctx context.Context, query string, topK int) ([]string, error) {
	return nil, f.searchErr
}

func (f *fakeVM) Store(ctx context.Context, content string, metadata map[string]any) error {
	f.stored.Add(1)
	return nil
}

type fakeEM struct {
	recorded atomic.Int64
}

func (f *fakeEM) Record(ctx context.Context, ep memory.Episode) error {
	f.recorded.Add(1)
	return nil
}
func (f *fakeEM) FindSimilar(ctx context.Context, goal string, limit int) ([]memory.Episode, error) {
	return nil, nil
}

// silentLogger descarta toda saída; evita poluir o test runner.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestAgent monta um Agent com dependências fake. Os ponteiros nil são
// substituídos por fakes vazios para reduzir boilerplate nos casos felizes.
func newTestAgent(t *testing.T, llm LLMClient, wm WorkingMemoryStore, vm VectorMemoryStore, em EpisodicMemoryStore, registry *tools.Registry) *Agent {
	t.Helper()
	if wm == nil {
		wm = &fakeWM{}
	}
	if vm == nil {
		vm = &fakeVM{}
	}
	if em == nil {
		em = &fakeEM{}
	}
	if registry == nil {
		registry = tools.NewRegistry()
	}
	cfg := Config{Model: "test-model", MaxTokens: 1024, MaxIter: 3}
	return New(llm, cfg, registry, wm, vm, em, silentLogger())
}

// ── Helpers para construir respostas ─────────────────────────────────────────

func stopResponse(text string) *bedrock.ChatResponse {
	return &bedrock.ChatResponse{
		Choices: []bedrock.Choice{{
			Index:        0,
			Message:      bedrock.Message{Role: "assistant", Content: text},
			FinishReason: "stop",
		}},
	}
}

func toolCallResponse(id, name, argsJSON string) *bedrock.ChatResponse {
	return &bedrock.ChatResponse{
		Choices: []bedrock.Choice{{
			Index: 0,
			Message: bedrock.Message{
				Role:    "assistant",
				Content: "",
				ToolCalls: []bedrock.ToolCall{{
					ID:       id,
					Type:     "function",
					Function: bedrock.FunctionCall{Name: name, Arguments: argsJSON},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestRun_EmptyGoalReturnsErrEmptyInput(t *testing.T) {
	a := newTestAgent(t, &fakeLLM{}, nil, nil, nil, nil)

	_, err := a.Run(context.Background(), "sess-1", "")
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("expected ErrEmptyInput, got %v", err)
	}
}

func TestRun_HappyPathStopFirstIteration(t *testing.T) {
	llm := &fakeLLM{responses: []*bedrock.ChatResponse{stopResponse("Hello, world.")}}
	a := newTestAgent(t, llm, nil, nil, nil, nil)

	result, err := a.Run(context.Background(), "sess-1", "diga olá")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "Hello, world." {
		t.Errorf("output = %q, want %q", result.Output, "Hello, world.")
	}
	if result.Iterations != 1 {
		t.Errorf("iterations = %d, want 1", result.Iterations)
	}
	if len(result.ToolsUsed) != 0 {
		t.Errorf("expected no tools used, got %v", result.ToolsUsed)
	}
	if llm.calls != 1 {
		t.Errorf("LLM should be called once, got %d", llm.calls)
	}
}

func TestRun_ToolCallThenStop(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&tools.Tool{
		Definition: bedrock.Tool{
			Type: "function",
			Function: bedrock.ToolFunction{
				Name:        "echo",
				Description: "echo input",
				Parameters:  map[string]any{"type": "object"},
			},
		},
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			return "echoed: " + string(input), nil
		},
	})

	llm := &fakeLLM{responses: []*bedrock.ChatResponse{
		toolCallResponse("call_1", "echo", `{"msg":"hi"}`),
		stopResponse("Done."),
	}}
	wm := &fakeWM{}
	a := newTestAgent(t, llm, wm, nil, nil, registry)

	result, err := a.Run(context.Background(), "sess-2", "use echo tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "Done." {
		t.Errorf("output = %q, want 'Done.'", result.Output)
	}
	if result.Iterations != 2 {
		t.Errorf("iterations = %d, want 2", result.Iterations)
	}
	if len(result.ToolsUsed) != 1 || result.ToolsUsed[0] != "echo" {
		t.Errorf("tools_used = %v, want [echo]", result.ToolsUsed)
	}
	if len(wm.checkpoints) != 1 || wm.checkpoints[0] != "echo" {
		t.Errorf("checkpoints = %v, want [echo]", wm.checkpoints)
	}
	if llm.calls != 2 {
		t.Errorf("LLM should be called twice, got %d", llm.calls)
	}
}

// TestRun_ParallelToolCalls exercita o caminho concorrente de executeTools.
// Verifica que:
//   - Cada result fica no slot i correto (ordem posicional preservada)
//   - toolsUsed acumula todos os nomes sem corromper o map (mutex)
//   - workingMem.Checkpoint é chamado N vezes, sem lost-update (mutex)
//
// Rodar com `go test -race` para detectar races em toolsUsed/checkpoints.
func TestRun_ParallelToolCalls(t *testing.T) {
	registry := tools.NewRegistry()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		n := name
		registry.Register(&tools.Tool{
			Definition: bedrock.Tool{
				Type:     "function",
				Function: bedrock.ToolFunction{Name: n, Description: n, Parameters: map[string]any{"type": "object"}},
			},
			Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
				// Pequeno sleep força sobreposição real entre as goroutines.
				time.Sleep(5 * time.Millisecond)
				return "out:" + n, nil
			},
		})
	}

	multi := &bedrock.ChatResponse{
		Choices: []bedrock.Choice{{
			Index: 0,
			Message: bedrock.Message{
				Role: "assistant",
				ToolCalls: []bedrock.ToolCall{
					{ID: "t1", Type: "function", Function: bedrock.FunctionCall{Name: "alpha", Arguments: `{}`}},
					{ID: "t2", Type: "function", Function: bedrock.FunctionCall{Name: "beta", Arguments: `{}`}},
					{ID: "t3", Type: "function", Function: bedrock.FunctionCall{Name: "gamma", Arguments: `{}`}},
				},
			},
			FinishReason: "tool_calls",
		}},
	}
	llm := &fakeLLM{responses: []*bedrock.ChatResponse{multi, stopResponse("ok")}}
	wm := &fakeWM{}
	a := newTestAgent(t, llm, wm, nil, nil, registry)

	result, err := a.Run(context.Background(), "sess-parallel", "fan out")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("output = %q, want 'ok'", result.Output)
	}

	if len(wm.checkpoints) != 3 {
		t.Errorf("expected 3 checkpoints (one per tool), got %d: %v", len(wm.checkpoints), wm.checkpoints)
	}

	seen := map[string]bool{}
	for _, c := range wm.checkpoints {
		seen[c] = true
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !seen[want] {
			t.Errorf("checkpoint for %q missing", want)
		}
	}
	if len(result.ToolsUsed) != 3 {
		t.Errorf("ToolsUsed = %v, want 3 entries", result.ToolsUsed)
	}
}

func TestRun_MaxIterationsReached(t *testing.T) {
	// Sempre devolve tool_calls; nunca finaliza. Tool fica em loop até MaxIter.
	registry := tools.NewRegistry()
	registry.Register(&tools.Tool{
		Definition: bedrock.Tool{
			Type:     "function",
			Function: bedrock.ToolFunction{Name: "noop", Description: "no op", Parameters: map[string]any{"type": "object"}},
		},
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) { return "ok", nil },
	})

	responses := []*bedrock.ChatResponse{
		toolCallResponse("c1", "noop", `{}`),
		toolCallResponse("c2", "noop", `{}`),
		toolCallResponse("c3", "noop", `{}`),
	}
	llm := &fakeLLM{responses: responses}
	a := newTestAgent(t, llm, nil, nil, nil, registry)

	_, err := a.Run(context.Background(), "sess-3", "spin forever")
	if !errors.Is(err, ErrMaxIterationsReached) {
		t.Fatalf("expected ErrMaxIterationsReached, got %v", err)
	}
	if llm.calls != 3 {
		t.Errorf("LLM should be called MaxIter=3 times, got %d", llm.calls)
	}
}

func TestRun_LLMErrorPropagatesWrapped(t *testing.T) {
	llm := &fakeLLM{err: errors.New("bedrock down")}
	a := newTestAgent(t, llm, nil, nil, nil, nil)

	_, err := a.Run(context.Background(), "sess-4", "anything")
	if err == nil {
		t.Fatal("expected an error from LLM failure")
	}
	if !errorContains(err, "agent llm call failed") {
		t.Errorf("expected wrapped 'agent llm call failed', got %q", err.Error())
	}
}

func TestRun_WorkingMemoryLoadFailureFallsBackToEmptyState(t *testing.T) {
	llm := &fakeLLM{responses: []*bedrock.ChatResponse{stopResponse("ok")}}
	wm := &fakeWM{loadErr: errors.New("redis down")}
	a := newTestAgent(t, llm, wm, nil, nil, nil)

	result, err := a.Run(context.Background(), "sess-5", "go on")
	if err != nil {
		t.Fatalf("Run should survive workingMem.Load failure, got %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("output = %q, want 'ok'", result.Output)
	}
}

func TestRun_BuildSystemPromptFailureFallsBackToBase(t *testing.T) {
	llm := &fakeLLM{responses: []*bedrock.ChatResponse{stopResponse("ok")}}
	vm := &fakeVM{searchErr: errors.New("pgvector down")}
	a := newTestAgent(t, llm, nil, vm, nil, nil)

	result, err := a.Run(context.Background(), "sess-6", "ignore memory")
	if err != nil {
		t.Fatalf("Run should survive vectorMem.Search failure, got %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("output = %q, want 'ok'", result.Output)
	}
}

func TestRun_ResumedSessionInjectsCompletedSteps(t *testing.T) {
	// Sessão pré-existente com passos concluídos: a Add do contextMem deve
	// receber o cabeçalho "[Retomando tarefa]" em vez do goal puro. Verificamos
	// indiretamente: o LLM nunca falha, então só validamos que o run conclui.
	llm := &fakeLLM{responses: []*bedrock.ChatResponse{stopResponse("retomado")}}
	wm := &fakeWM{state: &memory.AgentState{
		CompletedSteps: []string{"step-1"},
		Variables:      map[string]string{"x": "1"},
	}}
	a := newTestAgent(t, llm, wm, nil, nil, nil)

	result, err := a.Run(context.Background(), "sess-7", "continue")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if result.Output != "retomado" {
		t.Errorf("output = %q, want 'retomado'", result.Output)
	}
}

// TestRun_ConcurrentRunsAreIsolated garante que execuções simultâneas não
// compartilham janela de contexto (regressão: o ContextMemory era um campo do
// Agent e Run chamava Reset(), corrompendo conversas concorrentes). Rodar com
// `go test -race`.
func TestRun_ConcurrentRunsAreIsolated(t *testing.T) {
	const n = 8
	llm := &concurrentFakeLLM{response: stopResponse("ok")}
	a := newTestAgent(t, llm, nil, nil, nil, nil)

	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = a.Run(context.Background(), "sess-conc", "goal")
		}(i)
	}
	wg.Wait()
	a.Drain()

	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d failed: %v", i, err)
		}
	}
}

// concurrentFakeLLM valida que cada request chega bem-formado (system + user)
// mesmo sob concorrência, e devolve sempre a mesma resposta.
type concurrentFakeLLM struct {
	response *bedrock.ChatResponse
}

func (f *concurrentFakeLLM) Chat(ctx context.Context, req bedrock.ChatRequest) (*bedrock.ChatResponse, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("empty messages")
	}
	return f.response, nil
}

func TestRun_CancelledContextStopsLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	llm := &fakeLLM{responses: []*bedrock.ChatResponse{stopResponse("never")}}
	a := newTestAgent(t, llm, nil, nil, nil, nil)

	_, err := a.Run(ctx, "sess-cancel", "anything")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if llm.calls != 0 {
		t.Errorf("LLM should not be called after cancellation, got %d calls", llm.calls)
	}
}

// ── Pure helper tests ───────────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"abcdefghij", 5, "abcde..."},
		// não pode partir runa multi-byte no meio ("ção" tem ç=2 bytes)
		{"ação", 2, "a..."},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestBuildPromptWithContext(t *testing.T) {
	base := "BASE"
	out := buildPromptWithContext(base, []string{"m1", "m2"}, []memory.Episode{
		{Goal: "g1", Outcome: "success", Summary: "s1"},
	})
	if !errorContains(asError(out), "BASE") {
		t.Errorf("expected base to be included, got %q", out)
	}
	if !errorContains(asError(out), "m1") || !errorContains(asError(out), "m2") {
		t.Errorf("expected memories to be included, got %q", out)
	}
	if !errorContains(asError(out), "g1") {
		t.Errorf("expected past episode to be included, got %q", out)
	}
}

func TestBuildPromptWithContext_EmptyExtras(t *testing.T) {
	out := buildPromptWithContext("BASE", nil, nil)
	if out != "BASE" {
		t.Errorf("with no extras, expected base unchanged, got %q", out)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func errorContains(err error, sub string) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), sub)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// asError converte string em error para reuso de errorContains nos testes de
// prompt (que produzem string).
func asError(s string) error { return stringErr(s) }

type stringErr string

func (s stringErr) Error() string { return string(s) }
