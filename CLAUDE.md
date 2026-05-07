# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run locally (requires postgres + redis already up)
go run ./cmd/server

# Start only infrastructure
docker-compose up -d postgres redis

# Build binary
go build -ldflags="-w -s" -o agent ./cmd/server

# Run full stack with Docker
docker-compose up --build

# Run tests
go test ./...

# Run a single package's tests
go test ./internal/agent/...
```

## Architecture

Go AI agent service that uses **OpenRouter** as its sole LLM backend (chat + embeddings). Uses the OpenAI-compatible HTTP API directly — no Anthropic SDK.

### Request flow

`POST /v1/run` → `main.go` handler → `agent.Run(sessionID, goal)` → reasoning loop → `tools.Registry.Execute()` → LLM response → `RunResult`

### Agent reasoning loop (`internal/agent/agent.go`)

Iterates up to `MaxIter` (default 20) times: sends messages + tool definitions to OpenRouter, receives `tool_calls` or a final `stop`, dispatches tool calls through the registry, checkpoints progress in WorkingMemory, compacts context when needed. Returns on `finish_reason == "stop"` or `ErrMaxIterationsReached`.

### Memory system (`internal/memory/`)

Four independent layers composed in the agent:

| Type | Backing store | Purpose |
|------|--------------|---------|
| `ContextMemory` | in-process slice | Active message window; auto-compacts at 80% of `maxTokens` (default 180k) using claude-haiku as summarizer |
| `WorkingMemory` | Redis | Session state (`AgentState`) — goal, plan, completed steps, variables; enables resumption after crash |
| `VectorMemory` | Postgres + pgvector | Semantic recall via cosine similarity (HNSW index, threshold 0.75, topK 5); embeddings via OpenRouter |
| `EpisodicMemory` | Postgres | Full run history with FTS on goals; used to build system prompt from past relevant runs |

### MCP client (`internal/mcp/client.go`)

Spawns an external MCP server as a subprocess, communicates over stdio using JSON-RPC 2.0 (protocol version `2024-11-05`). `AsAgentTools()` converts MCP tool definitions into `tools.Tool` entries registered in the global registry. GitHub MCP is conditionally registered when `GITHUB_TOKEN` is set.

### Tools (`internal/tools/`)

`Registry` holds named `*Tool` values each with a JSON schema and an `Execute` func. Builtins: `run_code` (Python via `python3 -c`, Go via temp file + `go run`), `file_ops` (read/write/list under `FILES_BASE_DIR`).

## Key environment variables

| Variable | Default | Notes |
|----------|---------|-------|
| `OPENROUTER_API_KEY` | — | Required |
| `AGENT_MODEL` | `anthropic/claude-sonnet-4-6` | Chat model |
| `EMBED_MODEL` | `openai/text-embedding-3-small` | Embedding model |
| `DATABASE_URL` | `postgres://agent:secret@localhost:5432/agentdb` | Postgres with pgvector |
| `REDIS_ADDR` | `localhost:6379` | WorkingMemory backing store |
| `FILES_BASE_DIR` | `./files` | Sandbox for file_ops tool |
| `PORT` | `8080` | HTTP listen port |
| `GITHUB_TOKEN` | — | Optional; enables MCP GitHub tools |
| `OTEL_EXPORTER_JAEGER_ENDPOINT` | — | OpenTelemetry traces to Jaeger |

## Observability

- **Traces**: OpenTelemetry → Jaeger (`:16686`)
- **Metrics**: Prometheus scrapes `:8080/metrics` → Grafana (`:3000`, password `admin`)
- Grafana and Jaeger are included in `docker-compose.yml`

## Database schema

`VectorMemory.CreateSchema()` and `EpisodicMemory.CreateSchema()` are called at startup — no separate migration tool. The Postgres image is `pgvector/pgvector:pg16`, which ships the `vector` extension.
