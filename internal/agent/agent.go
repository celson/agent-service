package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/agent-service/internal/bedrock"
	"github.com/yourorg/agent-service/internal/memory"
	"github.com/yourorg/agent-service/internal/tools"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	ErrMaxIterationsReached = errors.New("agent: max iterations reached without completing task")
	ErrEmptyInput           = errors.New("agent: input cannot be empty")
)

type Config struct {
	Model     string
	MaxTokens int
	MaxIter   int
	// MaxContextTokens limita a janela de contexto por execução; ao atingir
	// 80% desse valor o histórico é compactado via sumarização.
	MaxContextTokens int
	BasePrompt       string
	// PersistTimeout limita a gravação assíncrona de memória episódica/vetorial
	// após cada run.
	PersistTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{
		Model:            bedrock.DefaultChatModel,
		MaxTokens:        4096,
		MaxIter:          20,
		MaxContextTokens: 180_000,
		PersistTimeout:   30 * time.Second,
	}
}

// withDefaults preenche campos zerados com os valores de DefaultConfig,
// permitindo configs parciais nas call sites.
func (c Config) withDefaults() Config {
	def := DefaultConfig()
	if c.Model == "" {
		c.Model = def.Model
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = def.MaxTokens
	}
	if c.MaxIter <= 0 {
		c.MaxIter = def.MaxIter
	}
	if c.MaxContextTokens <= 0 {
		c.MaxContextTokens = def.MaxContextTokens
	}
	if c.PersistTimeout <= 0 {
		c.PersistTimeout = def.PersistTimeout
	}
	return c
}

type Agent struct {
	llm         LLMClient
	cfg         Config
	registry    *tools.Registry
	workingMem  WorkingMemoryStore
	vectorMem   VectorMemoryStore
	episodicMem EpisodicMemoryStore
	tracer      trace.Tracer
	logger      *slog.Logger

	// persistWG rastreia as goroutines de persistência pós-run, permitindo
	// drenar gravações pendentes no shutdown via Drain.
	persistWG sync.WaitGroup
}

func New(
	llm LLMClient,
	cfg Config,
	registry *tools.Registry,
	workingMem WorkingMemoryStore,
	vectorMem VectorMemoryStore,
	episodicMem EpisodicMemoryStore,
	logger *slog.Logger,
) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{
		llm: llm, cfg: cfg.withDefaults(), registry: registry,
		workingMem: workingMem, vectorMem: vectorMem, episodicMem: episodicMem,
		tracer: otel.Tracer("agent"), logger: logger,
	}
}

type RunResult struct {
	Output     string
	Iterations int
	ToolsUsed  []string
	DurationMs int64
}

type RunOptions struct {
	SystemPrompt string
}

// Run executa o loop de raciocínio para um objetivo. É seguro para chamadas
// concorrentes: cada execução tem sua própria janela de contexto.
func (a *Agent) Run(ctx context.Context, sessionID, goal string, opts ...RunOptions) (*RunResult, error) {
	ctx, span := a.tracer.Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("session_id", sessionID),
			attribute.String("goal", goal),
		),
	)
	defer span.End()

	if strings.TrimSpace(goal) == "" {
		return nil, ErrEmptyInput
	}

	start := time.Now()

	contextMem := memory.NewContextMemory(a.cfg.MaxContextTokens)
	state := a.initializeContext(ctx, contextMem, sessionID, goal, opts)

	result, err := a.runLoop(ctx, contextMem, sessionID, state)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	result.DurationMs = time.Since(start).Milliseconds()

	a.persistWG.Add(1)
	go func() {
		defer a.persistWG.Done()
		persistCtx, cancel := context.WithTimeout(context.Background(), a.cfg.PersistTimeout)
		defer cancel()
		a.persistEpisode(persistCtx, sessionID, goal, result)
	}()
	return result, nil
}

// Drain espera as persistências assíncronas pendentes concluírem. Chamar no
// shutdown para não perder episódios de runs recém-terminados.
func (a *Agent) Drain() {
	a.persistWG.Wait()
}

func (a *Agent) runLoop(ctx context.Context, contextMem *memory.ContextMemory, sessionID string, state *memory.AgentState) (*RunResult, error) {
	result := &RunResult{}
	toolsUsed := map[string]struct{}{}

	for i := 0; i < a.cfg.MaxIter; i++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("agent: run cancelled at iteration %d: %w", i+1, err)
		}

		result.Iterations = i + 1

		if err := contextMem.CompactIfNeeded(ctx, a.llm); err != nil {
			a.logger.Warn("context compaction failed", "error", err)
		}

		req := bedrock.ChatRequest{
			Model:     a.cfg.Model,
			MaxTokens: a.cfg.MaxTokens,
			Messages:  contextMem.Messages(),
			Tools:     a.registry.Definitions(),
		}

		resp, err := a.llm.Chat(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("agent llm call failed: %w", err)
		}
		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("agent: empty response from model")
		}

		choice := resp.Choices[0]
		a.logger.Debug("agent step",
			"iteration", i+1,
			"finish_reason", choice.FinishReason,
			"total_tokens", resp.Usage.TotalTokens,
		)

		contextMem.AddAssistantMessage(choice.Message)

		if choice.FinishReason == "tool_calls" || len(choice.Message.ToolCalls) > 0 {
			toolResults := a.executeTools(ctx, sessionID, choice.Message.ToolCalls, state, toolsUsed)
			contextMem.AddToolResults(toolResults)
			continue
		}

		// "stop"/"end_turn", "length" (max_tokens) e qualquer outro finish
		// reason sem tool calls encerram o loop com o texto disponível.
		result.Output = choice.Message.Text()
		break
	}

	if result.Output == "" {
		return nil, ErrMaxIterationsReached
	}

	result.ToolsUsed = sortedKeys(toolsUsed)
	return result, nil
}

func (a *Agent) initializeContext(ctx context.Context, contextMem *memory.ContextMemory, sessionID, goal string, opts []RunOptions) *memory.AgentState {
	var systemPrompt string
	if len(opts) > 0 && opts[0].SystemPrompt != "" {
		systemPrompt = opts[0].SystemPrompt
	} else {
		var err error
		systemPrompt, err = a.buildSystemPrompt(ctx, goal)
		if err != nil {
			a.logger.Warn("failed to build system prompt from memory", "error", err)
			systemPrompt = a.basePrompt()
		}
	}

	state, err := a.workingMem.Load(ctx, sessionID)
	if err != nil || state == nil {
		state = &memory.AgentState{Variables: make(map[string]string)}
	}

	contextMem.SetSystem(systemPrompt)

	if len(state.CompletedSteps) > 0 {
		contextMem.Add("user", fmt.Sprintf(
			"[Retomando tarefa]\nObjetivo: %s\nPassos concluídos: %v\nVariáveis: %v",
			goal, state.CompletedSteps, state.Variables,
		))
	} else {
		contextMem.Add("user", goal)
	}

	return state
}

func (a *Agent) executeTools(
	ctx context.Context,
	sessionID string,
	toolCalls []bedrock.ToolCall,
	state *memory.AgentState,
	toolsUsed map[string]struct{},
) []bedrock.Message {
	results := make([]bedrock.Message, len(toolCalls))
	var wg sync.WaitGroup
	// mu protege toolsUsed (map writes) E o Checkpoint, que é um RMW não-atômico
	// no Redis (Load → mutate → Save). Sem essa serialização, dois Checkpoints
	// concorrentes na mesma sessão sofrem lost-update: o segundo escreve sobre
	// CompletedSteps/Iteration baseado num snapshot stale, perdendo o trabalho
	// do primeiro.
	var mu sync.Mutex

	wg.Add(len(toolCalls))
	for i, tc := range toolCalls {
		go func(i int, tc bedrock.ToolCall) {
			defer wg.Done()
			name := tc.Function.Name

			mu.Lock()
			toolsUsed[name] = struct{}{}
			mu.Unlock()

			a.logger.Info("executing tool", "name", name, "session", sessionID)

			output, err := a.registry.Execute(ctx, name, json.RawMessage(tc.Function.Arguments))
			if err != nil {
				a.logger.Error("tool execution failed", "tool", name, "error", err)
				results[i] = bedrock.Message{
					Role: "tool", ToolCallID: tc.ID,
					Content: fmt.Sprintf("error: %s", err.Error()),
				}
				return
			}

			mu.Lock()
			if err := a.workingMem.Checkpoint(ctx, sessionID, name, output); err != nil {
				a.logger.Warn("checkpoint failed", "tool", name, "error", err)
			}
			mu.Unlock()

			results[i] = bedrock.Message{
				Role: "tool", ToolCallID: tc.ID, Content: output,
			}
		}(i, tc)
	}
	wg.Wait()
	return results
}

func (a *Agent) basePrompt() string {
	if a.cfg.BasePrompt != "" {
		return a.cfg.BasePrompt
	}
	return baseSystemPrompt
}

func (a *Agent) buildSystemPrompt(ctx context.Context, goal string) (string, error) {
	memories, err := a.vectorMem.Search(ctx, goal, 5)
	if err != nil {
		return a.basePrompt(), err
	}
	past, err := a.episodicMem.FindSimilar(ctx, goal, 3)
	if err != nil {
		a.logger.Warn("episodic memory lookup failed", "error", err)
	}
	return buildPromptWithContext(a.basePrompt(), memories, past), nil
}

func (a *Agent) persistEpisode(ctx context.Context, sessionID, goal string, result *RunResult) {
	outcome := "success"
	if result.Output == "" {
		outcome = "failure"
	}
	ep := memory.Episode{
		SessionID: sessionID, Goal: goal, Outcome: outcome,
		Summary: truncate(result.Output, 500), ToolsUsed: result.ToolsUsed,
		DurationMs: result.DurationMs,
	}
	if err := a.episodicMem.Record(ctx, ep); err != nil {
		a.logger.Error("failed to record episode", "error", err)
	}
	if err := a.vectorMem.Store(ctx, result.Output, map[string]any{
		"goal": goal, "session_id": sessionID,
	}); err != nil {
		a.logger.Error("failed to store vector memory", "error", err)
	}
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// truncate corta s em no máximo n bytes sem partir uma runa UTF-8 no meio.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

const baseSystemPrompt = `Você é um agente de IA útil e preciso.
Você tem acesso a ferramentas para executar código e interagir com sistemas externos.
Sempre planeje antes de agir. Ao concluir uma tarefa, apresente o resultado de forma clara e objetiva.`

func buildPromptWithContext(base string, memories []string, past []memory.Episode) string {
	var builder strings.Builder
	builder.WriteString(base)

	if len(memories) > 0 {
		builder.WriteString("\n\n[Conhecimento relevante recuperado da memória]\n")
		for _, m := range memories {
			builder.WriteString("- ")
			builder.WriteString(m)
			builder.WriteString("\n")
		}
	}
	if len(past) > 0 {
		builder.WriteString("\n\n[Experiências anteriores similares]\n")
		for _, ep := range past {
			fmt.Fprintf(&builder, "- Objetivo: %s | Resultado: %s | Resumo: %s\n",
				ep.Goal, ep.Outcome, ep.Summary)
		}
	}
	return builder.String()
}
