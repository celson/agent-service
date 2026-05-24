package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/yourorg/agent-service/internal/bedrock"
	"github.com/yourorg/agent-service/internal/tools"
)

type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan jsonRPCResponse

	serverName string
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func New(command string, args ...string) (*Client, error) {
	return NewWithEnv(nil, command, args...)
}

func NewWithEnv(env map[string]string, command string, args ...string) (*Client, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start: %w", err)
	}

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdoutPipe),
		pending: make(map[int64]chan jsonRPCResponse),
	}

	go c.readLoop()

	if err := c.initialize(); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("mcp: initialize: %w", err)
	}
	return c, nil
}

func (c *Client) initialize() error {
	result, err := c.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "agent-service", "version": "1.0.0"},
		"capabilities":    map[string]any{},
	})
	if err != nil {
		return err
	}
	var info struct {
		ServerInfo map[string]any `json:"serverInfo"`
	}
	json.Unmarshal(result, &info)
	if name, ok := info.ServerInfo["name"].(string); ok {
		c.serverName = name
	}
	return c.notify("notifications/initialized", nil)
}

func (c *Client) ListTools(ctx context.Context) ([]MCPToolDef, error) {
	result, err := c.call("tools/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []MCPToolDef `json:"tools"`
	}
	json.Unmarshal(result, &resp)
	return resp.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	result, err := c.call("tools/call", map[string]any{
		"name": name, "arguments": args,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	json.Unmarshal(result, &resp)
	if resp.IsError && len(resp.Content) > 0 {
		return "", fmt.Errorf("mcp tool error: %s", resp.Content[0].Text)
	}
	var out string
	for _, c := range resp.Content {
		if c.Type == "text" {
			out += c.Text + "\n"
		}
	}
	return out, nil
}

func (c *Client) AsAgentTools(ctx context.Context) ([]*tools.Tool, error) {
	defs, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	agentTools := make([]*tools.Tool, len(defs))
	for i, def := range defs {
		def := def
		client := c
		agentTools[i] = &tools.Tool{
			Definition: bedrock.Tool{
				Type: "function",
				Function: bedrock.ToolFunction{
					Name:        def.Name,
					Description: def.Description,
					Parameters: map[string]any{
						"type":       def.InputSchema.Type,
						"properties": def.InputSchema.Properties,
						"required":   def.InputSchema.Required,
					},
				},
			},
			Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
				var args map[string]any
				json.Unmarshal(input, &args)
				return client.CallTool(ctx, def.Name, args)
			},
		}
	}
	return agentTools, nil
}

func (c *Client) Close() error {
	c.stdin.Close()
	return c.cmd.Wait()
}

func (c *Client) call(method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan jsonRPCResponse, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	req := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := json.NewEncoder(c.stdin).Encode(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: send: %w", err)
	}

	resp := <-ch
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func (c *Client) notify(method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	return json.NewEncoder(c.stdin).Encode(req)
}

func (c *Client) readLoop() {
	decoder := json.NewDecoder(c.stdout)
	for {
		var resp jsonRPCResponse
		if err := decoder.Decode(&resp); err != nil {
			return
		}
		if resp.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

type MCPToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	} `json:"inputSchema"`
}
