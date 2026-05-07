# Agent Service

Agente de IA em Go usando **OpenRouter** como backend único para chat e embeddings.
Sem dependência do SDK da Anthropic — usa HTTP direto com a API compatível com OpenAI.

## Modelo padrão

`anthropic/claude-sonnet-4-6` via OpenRouter.

## Arquitetura

```
cmd/server/main.go              — Entry point HTTP + wiring
internal/
  openrouter/client.go          — Cliente HTTP (chat + embeddings)
  agent/agent.go                — Loop raciocínio → ação → observação
  memory/
    context_memory.go           — Janela de tokens ativa (compactação automática)
    working_memory.go           — Estado de sessão no Redis
    vector_memory.go            — Memória semântica via pgvector + OpenRouter embeddings
    episodic_memory.go          — Histórico de runs no Postgres
  tools/
    registry.go                 — Registro de ferramentas
    builtin.go                  — run_code (Python + Go), file_ops
  mcp/client.go                 — Cliente MCP stdio (subprocess)
```

## Setup rápido

```bash
# 1. Copiar variáveis de ambiente
cp .env.example .env
# Preencher OPENROUTER_API_KEY

# 2. Subir infraestrutura
docker-compose up -d postgres redis

# 3. Rodar localmente
go run ./cmd/server

# 4. Ou subir tudo com Docker
docker-compose up --build
```

## Endpoints

### POST /v1/run

```bash
curl -X POST http://localhost:8080/v1/run \
  -H "Content-Type: application/json" \
  -d '{"session_id": "sess-001", "goal": "Escreva um script Python que calcule os primeiros 20 números de Fibonacci"}'
```

Resposta:
```json
{
  "session_id": "sess-001",
  "output": "...",
  "iterations": 2,
  "tools_used": ["run_code"],
  "duration_ms": 3200
}
```

### GET /v1/health — healthcheck
### GET /v1/stats  — métricas de runs (total, success rate, avg duration)

## Tools disponíveis

| Tool           | Descrição                                |
|----------------|------------------------------------------|
| `run_code`     | Executa Python ou Go em sandbox local    |
| `file_ops`     | Lê, escreve e lista arquivos             |
| `recall_memory`| Busca semântica nas memórias do agente   |

Web search está desabilitada por padrão. Para habilitar, adicione `NewWebSearchTool()` em `cmd/server/main.go`.

## Memória

| Camada       | Backend   | Duração    | Uso                              |
|--------------|-----------|------------|----------------------------------|
| In-context   | Tokens    | Por turno  | Conversa ativa (compacta via Haiku) |
| Working      | Redis     | 24h TTL    | Estado e checkpoints de tarefas  |
| Semântica    | pgvector  | Permanente | Conhecimento indexado por embedding |
| Episódica    | Postgres  | Permanente | Histórico de runs e auditoria    |

## Observabilidade

| Serviço    | URL                    |
|------------|------------------------|
| Jaeger     | http://localhost:16686 |
| Prometheus | http://localhost:9090  |
| Grafana    | http://localhost:3000  |
