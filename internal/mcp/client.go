// Package mcp implementa um cliente MCP (Model Context Protocol) que conversa
// com um servidor externo via stdio usando JSON-RPC 2.0.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed indica que a conexão com o servidor MCP foi encerrada (processo
// morreu ou stdout fechou) enquanto havia chamadas pendentes.
var ErrClosed = errors.New("mcp: connection closed")

const (
	protocolVersion = "2024-11-05"
	// initTimeout limita o handshake inicial; um servidor que não responde
	// ao initialize não pode bloquear o boot do serviço.
	initTimeout = 30 * time.Second
)

type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan jsonRPCResponse
	// done é fechado quando o readLoop termina (servidor caiu ou Close);
	// desbloqueia chamadas pendentes em vez de deixá-las penduradas para sempre.
	done chan struct{}

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
		done:    make(chan struct{}),
	}

	go c.readLoop()

	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	if err := c.initialize(ctx); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("mcp: initialize: %w", err)
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context) error {
	result, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"clientInfo":      map[string]any{"name": "agent-service", "version": "1.0.0"},
		"capabilities":    map[string]any{},
	})
	if err != nil {
		return err
	}
	var info struct {
		ServerInfo map[string]any `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &info); err == nil {
		if name, ok := info.ServerInfo["name"].(string); ok {
			c.serverName = name
		}
	}
	return c.notify("notifications/initialized", nil)
}

// ServerName devolve o nome anunciado pelo servidor no handshake (pode ser vazio).
func (c *Client) ServerName() string { return c.serverName }

func (c *Client) ListTools(ctx context.Context) ([]MCPToolDef, error) {
	result, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []MCPToolDef `json:"tools"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("mcp: parse tools/list: %w", err)
	}
	return resp.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	result, err := c.call(ctx, "tools/call", map[string]any{
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
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("mcp: parse tools/call result: %w", err)
	}
	if resp.IsError && len(resp.Content) > 0 {
		return "", fmt.Errorf("mcp tool error: %s", resp.Content[0].Text)
	}
	var out strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
			out.WriteByte('\n')
		}
	}
	return out.String(), nil
}

func (c *Client) Close() error {
	_ = c.stdin.Close()
	return c.cmd.Wait()
}

// call envia uma requisição JSON-RPC e espera a resposta correspondente.
// Desbloqueia se ctx for cancelado ou se a conexão com o servidor cair.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan jsonRPCResponse, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	removePending := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	req := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := json.NewEncoder(c.stdin).Encode(req); err != nil {
		removePending()
		return nil, fmt.Errorf("mcp: send: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		removePending()
		return nil, fmt.Errorf("mcp: %s: %w", method, ctx.Err())
	case <-c.done:
		removePending()
		return nil, fmt.Errorf("mcp: %s: %w", method, ErrClosed)
	}
}

func (c *Client) notify(method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	return json.NewEncoder(c.stdin).Encode(req)
}

func (c *Client) readLoop() {
	// Ao sair (EOF/erro de decode), fecha done para liberar todos os calls
	// bloqueados — sem isso, um servidor que morre deixa goroutines penduradas.
	defer close(c.done)

	decoder := json.NewDecoder(c.stdout)
	for {
		var resp jsonRPCResponse
		if err := decoder.Decode(&resp); err != nil {
			return
		}
		if resp.ID == 0 {
			// Notificação ou request do servidor; este cliente não os trata.
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
