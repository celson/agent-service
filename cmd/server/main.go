package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/yourorg/agent-service/internal/agent"
	"github.com/yourorg/agent-service/internal/mcp"
	"github.com/yourorg/agent-service/internal/memory"
	"github.com/yourorg/agent-service/internal/openrouter"
	"github.com/yourorg/agent-service/internal/tools"
)

func main() {
	godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── OpenRouter ────────────────────────────────────────────────────────────
	orClient := openrouter.New(
		mustEnv("OPENROUTER_API_KEY"),
		openrouter.WithAppName("agent-service"),
	)

	// ── Dependências externas ─────────────────────────────────────────────────
	pgPool, err := pgxpool.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		logger.Error("postgres connect failed", "error", err)
		os.Exit(1)
	}
	defer pgPool.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr:     mustEnv("REDIS_ADDR"),
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	defer redisClient.Close()

	// ── Memórias ──────────────────────────────────────────────────────────────
	embedModel := envOrDefault("EMBED_MODEL", openrouter.DefaultEmbedModel)

	vectorMem := memory.NewVectorMemory(pgPool, orClient, embedModel)
	episodicMem := memory.NewEpisodicMemory(pgPool)
	workingMem := memory.NewWorkingMemory(redisClient, 24*time.Hour)
	contextMem := memory.NewContextMemory(180_000)

	if err := vectorMem.CreateSchema(ctx); err != nil {
		logger.Warn("vector schema creation failed", "error", err)
	}
	if err := episodicMem.CreateSchema(ctx); err != nil {
		logger.Warn("episodic schema creation failed", "error", err)
	}

	// ── Tools ─────────────────────────────────────────────────────────────────
	registry := tools.NewRegistry()
	registry.Register(tools.NewCodeRunnerTool())
	registry.Register(tools.NewFileOpsTool(envOrDefault("FILES_BASE_DIR", "/app/files")))

	// Tool de recall semântico
	registry.Register(&tools.Tool{
		Definition: openrouter.Tool{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        "recall_memory",
				Description: "Busca em memórias de longo prazo por relevância semântica",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "O que buscar na memória",
						},
					},
					"required": []string{"query"},
				},
			},
		},
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			var params struct {
				Query string `json:"query"`
			}
			json.Unmarshal(input, &params)
			results, err := vectorMem.Search(ctx, params.Query, 5)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "Nenhuma memória relevante encontrada.", nil
			}
			out := "Memórias encontradas:\n"
			for i, r := range results {
				out += fmt.Sprintf("%d. %s\n", i+1, r)
			}
			return out, nil
		},
	})

	// ── MCP Servers opcionais ─────────────────────────────────────────────────
	if os.Getenv("GITHUB_TOKEN") != "" {
		mcpClient, err := mcp.New("npx", "-y", "@modelcontextprotocol/server-github")
		if err != nil {
			logger.Warn("github mcp server failed to start", "error", err)
		} else {
			defer mcpClient.Close()
			agentTools, err := mcpClient.AsAgentTools(ctx)
			if err != nil {
				logger.Warn("failed to load github mcp tools", "error", err)
			} else {
				for _, t := range agentTools {
					registry.Register(t)
				}
				logger.Info("github mcp tools registered", "count", len(agentTools))
			}
		}
	}

	// ── MCP Grafana ───────────────────────────────────────────────────────────
	if grafanaURL := os.Getenv("GRAFANA_URL"); grafanaURL != "" {
		mcpGrafana, err := mcp.NewWithEnv(
			map[string]string{
				"GRAFANA_URL":                   grafanaURL,
				"GRAFANA_SERVICE_ACCOUNT_TOKEN": os.Getenv("GRAFANA_SERVICE_ACCOUNT_TOKEN"),
			},
			"uvx", "mcp-grafana",
		)
		if err != nil {
			logger.Warn("grafana mcp server failed to start", "error", err)
		} else {
			defer mcpGrafana.Close()
			grafanaTools, err := mcpGrafana.AsAgentTools(ctx)
			if err != nil {
				logger.Warn("failed to load grafana mcp tools", "error", err)
			} else {
				for _, t := range grafanaTools {
					registry.Register(t)
				}
				logger.Info("grafana mcp tools registered", "count", len(grafanaTools))
			}
		}
	}

	// ── Agente ────────────────────────────────────────────────────────────────
	agentCfg := agent.Config{
		Model:     envOrDefault("AGENT_MODEL", openrouter.DefaultChatModel),
		MaxTokens: 4096,
		MaxIter:   20,
	}

	a := agent.New(orClient, agentCfg, registry, contextMem, workingMem, vectorMem, episodicMem, logger)

	// ── HTTP Server ───────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SessionID string `json:"session_id"`
			Goal      string `json:"goal"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Goal == "" {
			http.Error(w, "goal is required", http.StatusBadRequest)
			return
		}
		if req.SessionID == "" {
			req.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())
		}

		result, err := a.Run(r.Context(), req.SessionID, req.Goal)
		if err != nil {
			if errors.Is(err, agent.ErrMaxIterationsReached) {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			logger.Error("agent run failed", "error", err, "session", req.SessionID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"session_id":  req.SessionID,
			"output":      result.Output,
			"iterations":  result.Iterations,
			"tools_used":  result.ToolsUsed,
			"duration_ms": result.DurationMs,
		})
	})

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		stats, err := episodicMem.Stats(r.Context())
		if err != nil {
			http.Error(w, "failed to get stats", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})

	port := envOrDefault("PORT", "8080")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutCancel()
		srv.Shutdown(shutCtx)
		cancel()
	}()

	logger.Info("server starting", "port", port, "model", agentCfg.Model)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "required env var %s is not set\n", key)
		os.Exit(1)
	}
	return v
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
