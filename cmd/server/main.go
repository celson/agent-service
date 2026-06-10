package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/yourorg/agent-service/internal/agent"
	"github.com/yourorg/agent-service/internal/bedrock"
	"github.com/yourorg/agent-service/internal/config"
	"github.com/yourorg/agent-service/internal/mcp"
	"github.com/yourorg/agent-service/internal/memory"
	"github.com/yourorg/agent-service/internal/tools"
)

const (
	defaultDatabaseURL = "postgres://agent:secret@localhost:5432/agentdb"
	defaultRedisAddr   = "localhost:6379"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run concentra a inicialização para que defers rodem antes do exit —
// os.Exit direto em main pulava o cleanup de pool/redis/MCP.
func run() error {
	godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── AWS Bedrock ───────────────────────────────────────────────────────────
	// Usa a default credentials chain do AWS SDK Go v2: env vars, sessões SSO
	// (aws sso login), ~/.aws/credentials, assume-role, e roles de container/IMDS.
	llm, err := bedrock.New(ctx,
		envOrDefault("AWS_REGION", bedrock.DefaultRegion),
		bedrock.WithAppName("agent-service"),
	)
	if err != nil {
		return fmt.Errorf("bedrock init failed: %w", err)
	}

	// ── Dependências externas ─────────────────────────────────────────────────
	pgPool, err := pgxpool.New(ctx, envOrDefault("DATABASE_URL", defaultDatabaseURL))
	if err != nil {
		return fmt.Errorf("postgres connect failed: %w", err)
	}
	defer pgPool.Close()

	if err := pgPool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping failed: %w", err)
	}

	redisOpts := &redis.Options{
		Addr:     envOrDefault("REDIS_ADDR", defaultRedisAddr),
		Password: os.Getenv("REDIS_PASSWORD"),
	}
	if os.Getenv("REDIS_TLS_ENABLED") == "true" {
		redisOpts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}

	// ── Memórias ──────────────────────────────────────────────────────────────
	embedModel := envOrDefault("BEDROCK_EMBED_MODEL", bedrock.DefaultEmbedModel)

	vectorMem := memory.NewVectorMemory(pgPool, llm, embedModel)
	episodicMem := memory.NewEpisodicMemory(pgPool)
	workingMem := memory.NewWorkingMemory(redisClient, 24*time.Hour)

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
	registry.Register(newRecallMemoryTool(vectorMem))

	// ── MCP Servers via .mcp.json ────────────────────────────────────────────
	mcpCfg, err := config.LoadMCP(".mcp.json")
	if err != nil {
		logger.Warn("failed to load .mcp.json", "error", err)
	} else {
		for name, srv := range mcpCfg.Servers {
			// Pula servers cujas env vars obrigatórias estão vazias
			if hasEmptyRequiredEnv(srv.Env) {
				logger.Info("mcp server skipped (missing env vars)", "server", name)
				continue
			}
			mcpClient, err := mcp.NewWithEnv(srv.Env, srv.Command, srv.Args...)
			if err != nil {
				logger.Warn("mcp server failed to start", "server", name, "error", err)
				continue
			}
			defer mcpClient.Close()
			agentTools, err := mcpClient.AsAgentTools(ctx)
			if err != nil {
				logger.Warn("failed to load mcp tools", "server", name, "error", err)
				continue
			}
			for _, t := range agentTools {
				registry.Register(t)
			}
			logger.Info("mcp tools registered", "server", name, "count", len(agentTools))
		}
	}

	// ── Prompts e skills externos ─────────────────────────────────────────────
	basePrompt := config.LoadPrompt("prompts/base.md", "")
	srePrompt := config.LoadPrompt("prompts/sre.md", "")

	troubleshootingSkill, err := config.LoadSkill("skills", "troubleshooting")
	if err != nil {
		logger.Warn("failed to load troubleshooting skill", "error", err)
		troubleshootingSkill = ""
	}

	// ── Agente ────────────────────────────────────────────────────────────────
	agentCfg := agent.Config{
		Model:            envOrDefault("BEDROCK_MODEL", bedrock.DefaultChatModel),
		MaxTokens:        envIntOrDefault("AGENT_MAX_TOKENS", 4096),
		MaxIter:          envIntOrDefault("AGENT_MAX_ITER", 20),
		MaxContextTokens: envIntOrDefault("AGENT_MAX_CONTEXT_TOKENS", 180_000),
		BasePrompt:       basePrompt,
	}

	a := agent.New(llm, agentCfg, registry, workingMem, vectorMem, episodicMem, logger)

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
		if strings.TrimSpace(req.Goal) == "" {
			http.Error(w, "goal is required", http.StatusBadRequest)
			return
		}
		if req.SessionID == "" {
			req.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())
		}

		result, err := a.Run(r.Context(), req.SessionID, req.Goal)
		if err != nil {
			writeAgentError(w, logger, err, req.SessionID)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"session_id":  req.SessionID,
			"output":      result.Output,
			"iterations":  result.Iterations,
			"tools_used":  result.ToolsUsed,
			"duration_ms": result.DurationMs,
		})
	})

	mux.HandleFunc("POST /v1/alert", func(w http.ResponseWriter, r *http.Request) {
		var payload alertPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(payload.Alerts) == 0 {
			http.Error(w, "no alerts in payload", http.StatusBadRequest)
			return
		}

		alert := payload.Alerts[0]
		sessionID := "sre-alert-" + alert.Fingerprint
		goal := buildSREGoal(alert, troubleshootingSkill)

		result, err := a.Run(r.Context(), sessionID, goal, agent.RunOptions{
			SystemPrompt: srePrompt,
		})
		if err != nil {
			writeAgentError(w, logger, err, sessionID)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"session_id":  sessionID,
			"alert":       alert.Labels["alertname"],
			"rca":         result.Output,
			"iterations":  result.Iterations,
			"tools_used":  result.ToolsUsed,
			"duration_ms": result.DurationMs,
		})
	})

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		checks := map[string]string{"postgres": "ok", "redis": "ok"}
		status := http.StatusOK
		if err := pgPool.Ping(ctx); err != nil {
			checks["postgres"] = err.Error()
			status = http.StatusServiceUnavailable
		}
		if err := redisClient.Ping(ctx).Err(); err != nil {
			checks["redis"] = err.Error()
			status = http.StatusServiceUnavailable
		}

		overall := "ok"
		if status != http.StatusOK {
			overall = "degraded"
		}
		writeJSON(w, status, map[string]any{"status": overall, "checks": checks})
	})

	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		stats, err := episodicMem.Stats(r.Context())
		if err != nil {
			logger.Error("stats query failed", "error", err)
			http.Error(w, "failed to get stats", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	})

	port := envOrDefault("PORT", "8080")
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Warn("server shutdown error", "error", err)
		}
		// Espera persistências assíncronas de runs recém-concluídos.
		a.Drain()
		cancel()
	}()

	logger.Info("server starting", "port", port, "model", agentCfg.Model)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// newRecallMemoryTool expõe a busca semântica do VectorMemory como tool.
func newRecallMemoryTool(vectorMem *memory.VectorMemory) *tools.Tool {
	return &tools.Tool{
		Definition: bedrock.Tool{
			Type: "function",
			Function: bedrock.ToolFunction{
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
			params, err := tools.ParseInput[struct {
				Query string `json:"query"`
			}](input)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(params.Query) == "" {
				return "", fmt.Errorf("recall_memory: query is required")
			}
			results, err := vectorMem.Search(ctx, params.Query, 5)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "Nenhuma memória relevante encontrada.", nil
			}
			var sb strings.Builder
			sb.WriteString("Memórias encontradas:\n")
			for i, r := range results {
				fmt.Fprintf(&sb, "%d. %s\n", i+1, r)
			}
			return sb.String(), nil
		},
	}
}

// writeAgentError mapeia erros do agente para códigos HTTP apropriados.
func writeAgentError(w http.ResponseWriter, logger *slog.Logger, err error, sessionID string) {
	switch {
	case errors.Is(err, agent.ErrMaxIterationsReached):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, agent.ErrEmptyInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Cliente desistiu ou timeout: 499-like; usa 504 para deadline.
		http.Error(w, "request cancelled or timed out", http.StatusGatewayTimeout)
	default:
		logger.Error("agent run failed", "error", err, "session", sessionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type alertPayload struct {
	Alerts []alertItem `json:"alerts"`
}

type alertItem struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
	Fingerprint string            `json:"fingerprint"`
}

func buildSREGoal(a alertItem, skill string) string {
	alertName := a.Labels["alertname"]
	severity := a.Labels["severity"]
	service := a.Labels["service"]
	summary := a.Annotations["summary"]
	description := a.Annotations["description"]
	if description == "" {
		description = summary
	}

	var labels strings.Builder
	for k, v := range a.Labels {
		if k != "alertname" && k != "severity" && k != "service" {
			labels.WriteString("  ")
			labels.WriteString(k)
			labels.WriteString("=")
			labels.WriteString(v)
			labels.WriteString("\n")
		}
	}

	header := fmt.Sprintf(`ALERTA SRE: %s | Severity: %s | Status: %s
Serviço: %s | Início: %s
Descrição: %s
Labels adicionais:
%s`,
		alertName, severity, a.Status,
		service, a.StartsAt,
		description,
		labels.String(),
	)

	if skill != "" {
		return header + "\n\n" + skill
	}
	return header + fmt.Sprintf(`

Realize um Root Cause Analysis do serviço "%s":
1. Colete métricas (latência, erros, saturação, tráfego) no período do incidente
2. Busque logs de erro no Loki
3. Identifique a causa raiz com 5 Whys
4. Apresente: Root Cause | Impact | Timeline | Remediation Steps`, service)
}

// hasEmptyRequiredEnv retorna true se qualquer valor de env for vazio após expansão.
func hasEmptyRequiredEnv(env map[string]string) bool {
	for _, v := range env {
		if v == "" {
			return true
		}
	}
	return false
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
