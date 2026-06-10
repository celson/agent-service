package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yourorg/agent-service/internal/bedrock"
	"github.com/yourorg/agent-service/internal/tools"
)

// AsAgentTools lista as tools do servidor MCP e as converte em tools.Tool
// prontas para registro no Registry do agente.
func (c *Client) AsAgentTools(ctx context.Context) ([]*tools.Tool, error) {
	defs, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	agentTools := make([]*tools.Tool, len(defs))
	for i, def := range defs {
		def := def
		schemaType := def.InputSchema.Type
		if schemaType == "" {
			schemaType = "object"
		}
		agentTools[i] = &tools.Tool{
			Definition: bedrock.Tool{
				Type: "function",
				Function: bedrock.ToolFunction{
					Name:        def.Name,
					Description: def.Description,
					Parameters: map[string]any{
						"type":       schemaType,
						"properties": def.InputSchema.Properties,
						"required":   def.InputSchema.Required,
					},
				},
			},
			Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
				args := map[string]any{}
				if len(input) > 0 {
					if err := json.Unmarshal(input, &args); err != nil {
						return "", fmt.Errorf("mcp tool %s: invalid arguments: %w", def.Name, err)
					}
				}
				return c.CallTool(ctx, def.Name, args)
			},
		}
	}
	return agentTools, nil
}
