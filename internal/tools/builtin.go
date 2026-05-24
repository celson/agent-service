package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourorg/agent-service/internal/bedrock"
)

// ── Code Runner ───────────────────────────────────────────────────────────────

func NewCodeRunnerTool() *Tool {
	return &Tool{
		Definition: bedrock.Tool{
			Type: "function",
			Function: bedrock.ToolFunction{
				Name:        "run_code",
				Description: "Executa código em sandbox isolado. Suporta Python e Go.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"language": map[string]any{
							"type": "string",
							"enum": []string{"python", "go"},
						},
						"code": map[string]any{
							"type":        "string",
							"description": "Código a ser executado",
						},
						"timeout_seconds": map[string]any{
							"type":    "integer",
							"default": 30,
						},
					},
					"required": []string{"language", "code"},
				},
			},
		},
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			var params struct {
				Language       string `json:"language"`
				Code           string `json:"code"`
				TimeoutSeconds int    `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", err
			}
			if params.TimeoutSeconds == 0 {
				params.TimeoutSeconds = 30
			}
			timeout := time.Duration(params.TimeoutSeconds) * time.Second
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			switch params.Language {
			case "python":
				return runPython(ctx, params.Code)
			case "go":
				return runGo(ctx, params.Code)
			default:
				return "", fmt.Errorf("unsupported language: %s", params.Language)
			}
		},
	}
}

func runPython(ctx context.Context, code string) (string, error) {
	// Em produção: substituir por execução em container Docker isolado.
	// docker run --rm --network none --memory 128m python:3.12-alpine python -c "..."
	tmpFile, err := os.CreateTemp("", "agent-*.py")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(code)
	tmpFile.Close()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "python3", tmpFile.Name())
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("python error: %s", stderr.String())
		}
		return "", err
	}
	return stdout.String(), nil
}

func runGo(ctx context.Context, code string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "agent-go-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	mainFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(mainFile, []byte(code), 0600); err != nil {
		return "", err
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "go", "run", mainFile)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go error: %s", stderr.String())
	}
	return stdout.String(), nil
}

// ── File Operations ───────────────────────────────────────────────────────────

func NewFileOpsTool(baseDir string) *Tool {
	return &Tool{
		Definition: bedrock.Tool{
			Type: "function",
			Function: bedrock.ToolFunction{
				Name:        "file_ops",
				Description: "Lê, escreve e lista arquivos em um diretório base",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"operation": map[string]any{
							"type": "string",
							"enum": []string{"read", "write", "list"},
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Caminho relativo ao diretório base",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Conteúdo para escrita (apenas na operação write)",
						},
					},
					"required": []string{"operation", "path"},
				},
			},
		},
		Handler: fileOpsHandler(baseDir),
	}
}

func fileOpsHandler(baseDir string) Handler {
	return func(ctx context.Context, input json.RawMessage) (string, error) {
		var params struct {
			Operation string `json:"operation"`
			Path      string `json:"path"`
			Content   string `json:"content"`
		}
		if err := json.Unmarshal(input, &params); err != nil {
			return "", err
		}

		cleanPath := filepath.Clean(params.Path)
		if strings.HasPrefix(cleanPath, "..") {
			return "", fmt.Errorf("path traversal not allowed")
		}
		fullPath := filepath.Join(baseDir, cleanPath)

		switch params.Operation {
		case "read":
			data, err := os.ReadFile(fullPath)
			if err != nil {
				return "", fmt.Errorf("read: %w", err)
			}
			return string(data), nil

		case "write":
			if err := os.MkdirAll(filepath.Dir(fullPath), 0750); err != nil {
				return "", err
			}
			if err := os.WriteFile(fullPath, []byte(params.Content), 0600); err != nil {
				return "", fmt.Errorf("write: %w", err)
			}
			return fmt.Sprintf("escrito: %s (%d bytes)", params.Path, len(params.Content)), nil

		case "list":
			entries, err := os.ReadDir(fullPath)
			if err != nil {
				return "", fmt.Errorf("list: %w", err)
			}
			var lines []string
			for _, e := range entries {
				info, _ := e.Info()
				if e.IsDir() {
					lines = append(lines, fmt.Sprintf("[dir]  %s/", e.Name()))
				} else {
					lines = append(lines, fmt.Sprintf("[file] %s (%d bytes)", e.Name(), info.Size()))
				}
			}
			return strings.Join(lines, "\n"), nil

		default:
			return "", fmt.Errorf("unknown operation: %s", params.Operation)
		}
	}
}
