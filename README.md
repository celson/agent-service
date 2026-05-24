# Agent Service

Agente de IA em Go usando **AWS Bedrock** como backend único para chat e embeddings.
Autenticação via *default credentials chain* do AWS SDK — funciona com `aws sso login`,
`~/.aws/credentials`, env vars, ou IAM roles em produção. Sem API keys próprias.

## Modelo padrão

- **Chat:** `us.anthropic.claude-sonnet-4-6` (cross-region inference profile US)
- **Compactação de contexto:** `us.anthropic.claude-haiku-4-5`
- **Embeddings:** `amazon.titan-embed-text-v2:0` (1024 dims)

## Arquitetura

```
cmd/server/main.go              — Entry point HTTP + wiring
internal/
  bedrock/                      — Cliente AWS Bedrock (chat + embeddings)
  agent/agent.go                — Loop raciocínio → ação → observação
  config/
    mcp.go                      — Loader de .mcp.json e prompts externos
    skills.go                   — Loader de skills com resolução de dependências
  memory/
    context_memory.go           — Janela de tokens ativa (compactação automática)
    working_memory.go           — Estado de sessão no Redis
    vector_memory.go            — Memória semântica via pgvector + Titan v2 embeddings
    episodic_memory.go          — Histórico de runs no Postgres
  tools/
    registry.go                 — Registro de ferramentas
    builtin.go                  — run_code (Python + Go), file_ops
  mcp/client.go                 — Cliente MCP stdio (subprocess)
prompts/
  base.md                       — System prompt do agente genérico
  sre.md                        — System prompt do agente SRE
skills/
  collect-metrics.md            — Skill: coleta dos 4 golden signals via Grafana
  troubleshooting.md            — Skill: correlação de métricas e RCA (depende de collect-metrics)
.mcp.json                       — Configuração de MCP servers (sem código)
```

## Setup rápido

```bash
# 1. Logar no AWS CLI (SSO ou credenciais estáticas)
aws sso login            # ou: aws configure
export AWS_REGION=us-east-1
# (opcional) export AWS_PROFILE=meu-profile

# 2. Subir infraestrutura
docker-compose up -d postgres redis

# 3. Rodar localmente
go run ./cmd/server

# 4. Ou subir tudo com Docker
# (o compose monta ~/.aws como volume read-only no container)
docker-compose up --build
```

### Variáveis de ambiente

| Variável | Default | Descrição |
|----------|---------|-----------|
| `AWS_REGION` | `us-east-1` | Região AWS para Bedrock |
| `AWS_PROFILE` | `default` | Profile do `~/.aws/config` |
| `BEDROCK_MODEL` | `us.anthropic.claude-sonnet-4-6` | Modelo de chat |
| `BEDROCK_EMBED_MODEL` | `amazon.titan-embed-text-v2:0` | Modelo de embedding |
| `DATABASE_URL` | — | Postgres com pgvector |
| `REDIS_ADDR` | `localhost:6379` | Working memory |
| `FILES_BASE_DIR` | `./files` | Sandbox para `file_ops` |
| `PORT` | `8080` | Porta HTTP |
| `GITHUB_TOKEN` | — | Opcional; ativa MCP GitHub |

> **Credenciais:** o SDK Go v2 resolve nesta ordem: env vars (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN`) → sessão SSO (`~/.aws/sso/cache/`, populada por `aws sso login`) → `~/.aws/credentials` → assume-role → IMDS/IRSA.

## Endpoints

### POST /v1/run — agente genérico

```bash
curl -X POST http://localhost:8080/v1/run \
  -H "Content-Type: application/json" \
  -d '{"session_id": "sess-001", "goal": "Calcule os primeiros 20 números de Fibonacci"}'
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

### POST /v1/alert — agente SRE (webhook AlertManager / Grafana Alerting)

Recebe um evento de alerta, executa as skills de coleta de métricas e troubleshooting, e retorna um relatório de Root Cause Analysis.

```bash
curl -X POST http://localhost:8080/v1/alert \
  -H "Content-Type: application/json" \
  -d '{
    "alerts": [{
      "status": "firing",
      "labels": {"alertname": "HighErrorRate", "service": "api", "severity": "critical"},
      "annotations": {"description": "Taxa de erros 5xx acima do threshold"},
      "startsAt": "2026-05-08T10:00:00Z",
      "fingerprint": "abc123"
    }]
  }'
```

Resposta:
```json
{
  "session_id": "sre-alert-abc123",
  "alert": "HighErrorRate",
  "rca": "## Root Cause\n...\n## Impact\n...\n## Timeline\n...\n## Remediation Steps\n...",
  "iterations": 8,
  "tools_used": ["query_prometheus", "query_loki_logs", "get_annotations"],
  "duration_ms": 42000
}
```

O `session_id` é derivado do `fingerprint` do alerta — se o mesmo alerta disparar novamente, o agente retoma de onde parou (via WorkingMemory no Redis).

### GET /v1/health — healthcheck
### GET /v1/stats — métricas de runs (total, success rate, avg duration)

## Configuração de MCP Servers

MCP servers são configurados em `.mcp.json`, sem necessidade de alterar código. Valores `${VAR}` são expandidos a partir das variáveis de ambiente.

```json
{
  "mcpServers": {
    "grafana": {
      "command": "uvx",
      "args": ["mcp-grafana"],
      "env": {
        "GRAFANA_URL": "${GRAFANA_URL}",
        "GRAFANA_SERVICE_ACCOUNT_TOKEN": "${GRAFANA_SERVICE_ACCOUNT_TOKEN}"
      }
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_TOKEN": "${GITHUB_TOKEN}"
      }
    }
  }
}
```

Servers cujas env vars estiverem vazias são ignorados automaticamente na inicialização.

## Skills

Skills definem workflows reutilizáveis em arquivos `.md` dentro de `skills/`. Uma skill pode declarar dependências de outras skills via frontmatter — o loader resolve e concatena na ordem correta (dependências primeiro).

```
skills/
  collect-metrics.md    — 4 golden signals + logs + anotações de deploy
  troubleshooting.md    — correlação, 5 Whys, RCA (uses: collect-metrics)
```

**Formato de uma skill:**

```markdown
---
name: minha-skill
description: O que ela faz
uses: collect-metrics
---

## Skill: Minha Skill
...instruções para o agente...
```

Para criar uma nova skill, adicione o arquivo em `skills/` e carregue com:

```go
skill, err := config.LoadSkill("skills", "minha-skill")
```

## Prompts

Os system prompts ficam em `prompts/` e são carregados em runtime — editar o arquivo não exige recompilação nem restart.

| Arquivo            | Usado em           |
|--------------------|--------------------|
| `prompts/base.md`  | `POST /v1/run`     |
| `prompts/sre.md`   | `POST /v1/alert`   |

## Tools disponíveis (builtin)

| Tool            | Descrição                                |
|-----------------|------------------------------------------|
| `run_code`      | Executa Python ou Go em sandbox local    |
| `file_ops`      | Lê, escreve e lista arquivos             |
| `recall_memory` | Busca semântica nas memórias do agente   |

Tools adicionais são registradas automaticamente via MCP servers configurados em `.mcp.json`.

## Memória

| Camada     | Backend  | Duração    | Uso                                        |
|------------|----------|------------|--------------------------------------------|
| In-context | Tokens   | Por turno  | Conversa ativa (compacta via claude-haiku) |
| Working    | Redis    | 24h TTL    | Estado e checkpoints de tarefas            |
| Semântica  | pgvector | Permanente | Conhecimento indexado por embedding        |
| Episódica  | Postgres | Permanente | Histórico de runs e auditoria              |

## Arquitetura — Diagrama de fluxo

```mermaid
flowchart TD
    subgraph HTTP["HTTP :8080"]
        R1["POST /v1/run\ngoal + session_id"]
        R2["POST /v1/alert\nAlertManager webhook"]
        R3["GET /v1/health\nGET /v1/stats"]
    end

    subgraph Config["Configuração externa (sem recompilar)"]
        MCP[".mcp.json\nMCP servers + env vars"]
        PM["prompts/base.md\nprompts/sre.md"]
        SK["skills/collect-metrics.md\nskills/troubleshooting.md\n(uses: collect-metrics)"]
    end

    subgraph AgentLoop["Agent Loop (até 20 iterações)"]
        AL1["1. Monta mensagens\n+ tool definitions"]
        AL2["2. Chama AWS Bedrock\nclaude-sonnet-4-6"]
        AL3{"finish_reason?"}
        AL4["Executa tools\nvia Registry"]
        AL5["Checkpoint\nWorkingMemory"]
        AL6["Retorna output\n+ RunResult"]
        AL1 --> AL2 --> AL3
        AL3 -->|tool_calls| AL4 --> AL5 --> AL1
        AL3 -->|stop| AL6
    end

    subgraph Memory["Memória (4 camadas)"]
        M1["ContextMemory\nin-process · compacta via haiku"]
        M2["WorkingMemory\nRedis · 24h TTL"]
        M3["VectorMemory\nPostgres pgvector · cosine 0.75"]
        M4["EpisodicMemory\nPostgres · FTS"]
    end

    subgraph Tools["Tools Registry"]
        T1["run_code\nPython / Go sandbox"]
        T2["file_ops\n./files/"]
        T3["recall_memory\nbusca vetorial"]
        T4["Grafana MCP\nquery_prometheus\nquery_loki_logs\nget_annotations"]
        T5["GitHub MCP\n(opcional)"]
    end

    subgraph External["Serviços externos"]
        OR["AWS Bedrock\nchat + embeddings\n(Titan v2 / Claude Sonnet)"]
        GR["Grafana :3000"]
        RD["Redis :6379"]
        PG["Postgres + pgvector :5432"]
    end

    R1 -->|"system prompt ← prompts/base.md"| AgentLoop
    R2 -->|"1. carrega skills\n2. monta goal estruturado\n3. system prompt ← prompts/sre.md"| AgentLoop

    MCP -->|"spawn subprocess stdio\nJSON-RPC 2.0"| T4
    MCP --> T5
    PM --> R1
    PM --> R2
    SK --> R2

    AgentLoop <--> Memory
    AgentLoop --> Tools

    M1 <-->|compactação| OR
    M3 <-->|embeddings| OR
    M4 <--> PG
    M3 <--> PG
    M2 <--> RD
    AL2 <-->|chat| OR
    T4 <--> GR
    AL6 -->|"persiste episódio + vetor (async)"| M3
    AL6 --> M4
```

## Fluxo do alerta SRE

```mermaid
sequenceDiagram
    participant AM as AlertManager
    participant API as POST /v1/alert
    participant SK as Skills Loader
    participant AG as Agent Loop
    participant GR as Grafana MCP
    participant OR as AWS Bedrock

    AM->>API: {alerts: [{alertname, service, severity, startsAt}]}
    API->>SK: LoadSkill("troubleshooting")\n→ resolve uses: collect-metrics
    SK-->>API: collect-metrics.md + troubleshooting.md concatenados
    API->>AG: Run(session, goal=alert+skills, systemPrompt=sre.md)
    loop até 20 iterações
        AG->>OR: chat(messages, tools)
        OR-->>AG: tool_calls: [query_prometheus, query_loki_logs, ...]
        AG->>GR: query_prometheus(golden signals)
        GR-->>AG: métricas
        AG->>GR: query_loki_logs(LogQL)
        GR-->>AG: logs de erro
        AG->>OR: chat(tool results)
        OR-->>AG: stop → RCA report
    end
    AG-->>API: RunResult{output: "## Root Cause..."}
    API-->>AM: {rca, tools_used, iterations, duration_ms}
```

## Observabilidade

| Serviço    | URL                    |
|------------|------------------------|
| Jaeger     | http://localhost:16686 |
| Prometheus | http://localhost:9090  |
| Grafana    | http://localhost:3000  |
