// Package openrouter implementa um cliente HTTP para a API do OpenRouter,
// que é compatível com o formato OpenAI (chat completions + embeddings).
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	BaseURL            = "https://openrouter.ai/api/v1"
	DefaultChatModel   = "anthropic/claude-sonnet-4-6"
	DefaultEmbedModel  = "openai/text-embedding-3-small"
	defaultTimeout     = 5 * time.Minute
)

// Client é o cliente HTTP para o OpenRouter.
type Client struct {
	apiKey     string
	httpClient *http.Client
	appName    string // enviado como HTTP-Referer / X-Title
}

func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: defaultTimeout},
		appName:    "agent-service",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

type Option func(*Client)

func WithAppName(name string) Option { return func(c *Client) { c.appName = name } }
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.httpClient = &http.Client{Timeout: d} }
}

// ── Chat Completions ──────────────────────────────────────────────────────────

type Message struct {
	Role       string      `json:"role"`
	Content    any         `json:"content"` // string ou []ContentPart
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chat envia uma requisição de chat completion.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	var resp ChatResponse
	if err := c.post(ctx, "/chat/completions", req, &resp); err != nil {
		return nil, fmt.Errorf("openrouter chat: %w", err)
	}
	return &resp, nil
}

// ── Embeddings ────────────────────────────────────────────────────────────────

type EmbedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"` // string ou []string
}

type EmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed gera embeddings para um ou mais textos.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if model == "" {
		model = DefaultEmbedModel
	}

	var resp EmbedResponse
	if err := c.post(ctx, "/embeddings", EmbedRequest{Model: model, Input: inputs}, &resp); err != nil {
		return nil, fmt.Errorf("openrouter embed: %w", err)
	}

	result := make([][]float32, len(resp.Data))
	for _, d := range resp.Data {
		result[d.Index] = d.Embedding
	}
	return result, nil
}

// EmbedOne é um atalho para embedding de um único texto.
func (c *Client) EmbedOne(ctx context.Context, model, text string) ([]float32, error) {
	vecs, err := c.Embed(ctx, model, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return vecs[0], nil
}

// ── HTTP helper ───────────────────────────────────────────────────────────────

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://github.com/yourorg/agent-service")
	req.Header.Set("X-Title", c.appName)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		json.Unmarshal(respBody, &apiErr)
		return fmt.Errorf("status %d: %s", resp.StatusCode, apiErr.Error.Message)
	}

	return json.Unmarshal(respBody, out)
}
