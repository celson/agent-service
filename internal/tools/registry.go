package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/yourorg/agent-service/internal/bedrock"
)

type Handler func(ctx context.Context, input json.RawMessage) (string, error)

type Tool struct {
	Definition bedrock.Tool
	Handler    Handler
}

// Registry guarda as tools disponíveis para o agente. Seguro para uso
// concorrente: registros tardios (ex.: MCP servers que sobem depois) podem
// coexistir com Execute/Definitions chamados pelos runs em andamento.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

func (r *Registry) Register(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Definition.Function.Name] = t
}

func (r *Registry) Definitions() []bedrock.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]bedrock.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition)
	}
	return defs
}

func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	return t.Handler(ctx, input)
}

func ParseInput[T any](input json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(input, &v); err != nil {
		return v, fmt.Errorf("parse tool input: %w", err)
	}
	return v, nil
}
