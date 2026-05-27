package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	Model      string
	MaxTokens  int
	MaxIter    int
	BasePrompt string
}

func DefaultConfig() Config {
	return Config{
		Model:     bedrock.DefaultChatModel,
		MaxTokens: 4096,
		MaxIter:   20,
	}
}

type Agent struct {
	llm         LLMClient
	cfg         Config
	registry    *tools.Registry
	contextMem  *memory.ContextMemory
	workingMem  WorkingMemoryStore
	vectorMem   VectorMemoryStore
	episodicMem EpisodicMemoryStore
	tracer      trace.Tracer
	logger      *slog.Logger
}

func New(
	llm LLMClient,
	cfg Config,
	registry *tools.Registry,
	contextMem *memory.ContextMemory,
	workingMem WorkingMemoryStore,
	vectorMem VectorMemoryStore,
	episodicMem EpisodicMemoryStore,
	logger *slog.Logger,
) *Agent {
	return &Agent{
		llm: llm, cfg: cfg, registry: registry,
		contextMem: contextMem, workingMem: workingMem,
		vectorMem: vectorMem, episodicMem: episodicMem,
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

func (a *Agent) Run(ctx context.Context, sessionID, goal string, opts ...RunOptions) (*RunResult, error) {
	ctx, span := a.tracer.Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("session_id", sessionID),
			attribute.String("goal", goal),
		),
	)
	defer span.End()

	if goal == "" {
		return nil, ErrEmptyInput
	}

	start := time.Now()

	state := a.initializeContext(ctx, sessionID, goal, opts)

	result, err := a.runLoop(ctx, sessionID, state)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	result.DurationMs = time.Since(start).Milliseconds()

	go a.persistEpisode(context.Background(), sessionID, goal, result)
	return result, nil
}

func (a *Agent) runLoop(ctx context.Context, sessionID string, state *memory.AgentState) (*RunResult, error) {
	result := &RunResult{}
	toolsUsed := map[string]struct{}{}

	for i := 0; i < a.cfg.MaxIter; i++ {
		result.Iterations = i + 1

		if err := a.contextMem.CompactIfNeeded(ctx, a.llm); err != nil {
			a.logger.Warn("context compaction failed", "error", err)
		}

		req := bedrock.ChatRequest{
			Model:     a.cfg.Model,
			MaxTokens: a.cfg.MaxTokens,
			Messages:  a.contextMem.Messages(),
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

		a.contextMem.AddAssistantMessage(choice.Message)

		if choice.FinishReason == "end_turn" || choice.FinishReason == "stop" {
			result.Output = extractText(choice.Message)
			break
		}

		if choice.FinishReason == "tool_calls" || len(choice.Message.ToolCalls) > 0 {
			toolResults := a.executeTools(ctx, sessionID, choice.Message.ToolCalls, state, toolsUsed)
			a.contextMem.AddToolResults(toolResults)
			continue
		}

		result.Output = extractText(choice.Message)
		break
	}

	if result.Output == "" {
		return nil, ErrMaxIterationsReached
	}

	for t := range toolsUsed {
		result.ToolsUsed = append(result.ToolsUsed, t)
	}
	return result, nil
}

func (a *Agent) initializeContext(ctx context.Context, sessionID, goal string, opts []RunOptions) *memory.AgentState {
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
	if err != nil {
		state = &memory.AgentState{Variables: make(map[string]string)}
	}

	a.contextMem.Reset()
	a.contextMem.SetSystem(systemPrompt)

	if len(state.CompletedSteps) > 0 {
		a.contextMem.Add("user", fmt.Sprintf(
			"[Retomando tarefa]\nObjetivo: %s\nPassos concluídos: %v\nVariáveis: %v",
			goal, state.CompletedSteps, state.Variables,
		))
	} else {
		a.contextMem.Add("user", goal)
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
	var results []bedrock.Message
	for _, tc := range toolCalls {
		name := tc.Function.Name
		toolsUsed[name] = struct{}{}
		a.logger.Info("executing tool", "name", name)

		output, err := a.registry.Execute(ctx, name, json.RawMessage(tc.Function.Arguments))
		if err != nil {
			a.logger.Error("tool execution failed", "tool", name, "error", err)
			results = append(results, bedrock.Message{
				Role: "tool", ToolCallID: tc.ID,
				Content: fmt.Sprintf("error: %s", err.Error()),
			})
			continue
		}

		_ = a.workingMem.Checkpoint(ctx, sessionID, name, output)
		results = append(results, bedrock.Message{
			Role: "tool", ToolCallID: tc.ID, Content: output,
		})
	}
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
	past, _ := a.episodicMem.FindSimilar(ctx, goal, 3)
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

func extractText(msg bedrock.Message) string {
	switch v := msg.Content.(type) {
	case string:
		return v
	case []any:
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok {
						return t
					}
				}
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

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
