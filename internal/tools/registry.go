package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yourorg/agent-service/internal/openrouter"
)

type Handler func(ctx context.Context, input json.RawMessage) (string, error)

type Tool struct {
	Definition openrouter.Tool
	Handler    Handler
}

type Registry struct {
	tools map[string]*Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

func (r *Registry) Register(t *Tool) {
	r.tools[t.Definition.Function.Name] = t
}

func (r *Registry) Definitions() []openrouter.Tool {
	defs := make([]openrouter.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition)
	}
	return defs
}

func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t, ok := r.tools[name]
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
