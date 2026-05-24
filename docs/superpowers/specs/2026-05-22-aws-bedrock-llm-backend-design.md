# Migração do backend LLM: OpenRouter → AWS Bedrock

**Data:** 2026-05-22
**Status:** Aprovado (decisões tomadas via brainstorming)
**Branch:** `feat/aws-bedrock-llm`

## Contexto

O agent-service usa o OpenRouter (`internal/openrouter/`) como backend único para chat e embeddings. Vamos trocá-lo por AWS Bedrock para:

- Eliminar dependência de API key externa
- Aproveitar credenciais do AWS CLI já presentes no host do operador
- Consolidar com o padrão usado em outras ferramentas internas (kli usa Bedrock para chat Claude via AWS SDK Go v2)

## Decisões

| Item | Escolha |
|------|---------|
| Modelo de chat | `us.anthropic.claude-sonnet-4-6` (cross-region US, igual ao kli) |
| Modelo de embedding | `amazon.titan-embed-text-v2:0` (1024 dims) |
| Modelo de sumarização (context compaction) | `us.anthropic.claude-haiku-4-5` |
| Região AWS padrão | `us-east-1` |
| Schema pgvector | DROP + CREATE com `VECTOR(1024)` |
| Autenticação | `awsconfig.LoadDefaultConfig` (cadeia padrão AWS) — funciona com `aws sso login`, `~/.aws/credentials`, IAM role, env vars |

## Arquitetura

### Novo pacote `internal/bedrock/`

```
internal/bedrock/
  client.go      — Client struct, constructor com LoadDefaultConfig
  chat.go        — Chat() via bedrockruntime.InvokeModel
  embed.go       — Embed()/EmbedOne() via Titan v2
  translate.go   — OpenAI ↔ Anthropic-on-Bedrock conversion
  translate_test.go — Round-trip tests para mensagens, tools, finish_reason
```

### Tipos públicos

Mantemos os mesmos shapes do pacote `openrouter` (formato OpenAI-like):
`Client`, `Message`, `ContentPart`, `ToolCall`, `FunctionCall`, `Tool`, `ToolFunction`,
`ChatRequest`, `ChatResponse`, `Choice`, `Usage`.

A tradução para o formato Anthropic acontece **dentro** do pacote bedrock; o resto do código
(agent, memory, tools) não muda além do import path.

### Tradução de mensagens (OpenAI → Anthropic body)

- Msg `role: system` é extraída para o campo `system` top-level do body
- `role: user`/`assistant` com `Content: string` → `[{type: "text", text}]`
- Assistant com `ToolCalls[]` → `[{type: "tool_use", id, name, input: parsed_json(arguments)}]`
- `role: tool` → user turn com `[{type: "tool_result", tool_use_id, content}]`
- Tools OpenAI `{type, function: {name, description, parameters}}` → Anthropic
  `{name, description, input_schema: parameters}`

### Tradução de resposta (Anthropic → OpenAI)

- Content blocks `text` → concatenam em `Choice.Message.Content` (string)
- Content blocks `tool_use` → `Choice.Message.ToolCalls = [{id, type: "function", function: {name, arguments: json(input)}}]`
- `stop_reason`: `end_turn`/`stop_sequence` → `"stop"`; `tool_use` → `"tool_calls"`; `max_tokens` → `"length"`

### Embedding (Titan v2)

Bedrock Titan não tem batch — chamamos `InvokeModel` por texto:
- Body: `{"inputText": text}` → `{"embedding": [...], "inputTextTokenCount": N}`
- `Embed(model, []string)` faz loop sequencial; `EmbedOne` é atalho.

## Mudanças nas call sites

| Arquivo | Mudança |
|---------|---------|
| `cmd/server/main.go` | Constructor `bedrock.New(ctx, region, appName)`; env vars novas |
| `internal/agent/agent.go` | Import `openrouter` → `bedrock`; `DefaultChatModel` movida |
| `internal/memory/context_memory.go` | Import + modelo de sumarização `us.anthropic.claude-haiku-4-5` |
| `internal/memory/vector_memory.go` | Import + `DefaultEmbedModel` → Titan v2 |
| `internal/tools/registry.go`, `internal/tools/builtin.go` | Apenas import path |
| `internal/openrouter/` | **Deletar** |

## Schema (pgvector)

`vector_memory.go::CreateSchema` ganha um `DROP TABLE IF EXISTS memories` antes do CREATE,
e a coluna `embedding` passa de `VECTOR(1536)` para `VECTOR(1024)`.

Trade-off aceito: vetores antigos são descartados em dev. Episodic memory e working memory
ficam intactos.

## Variáveis de ambiente

| Removida | Adicionada | Default |
|----------|-----------|---------|
| `OPENROUTER_API_KEY` | — | — |
| `AGENT_MODEL` | `BEDROCK_MODEL` | `us.anthropic.claude-sonnet-4-6` |
| `EMBED_MODEL` | `BEDROCK_EMBED_MODEL` | `amazon.titan-embed-text-v2:0` |
| — | `AWS_REGION` (já existe na cadeia AWS) | `us-east-1` |

## Dockerfile / docker-compose

- Dockerfile: bump `golang:1.22-alpine` → `golang:1.25-alpine` (AWS SDK v2 v1.41 exige)
- docker-compose: troca env vars; monta `~/.aws:/root/.aws:ro` para credenciais funcionarem
  no container; remove `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`/`OPENROUTER_API_KEY`

## Erros e edge cases

- Sem credenciais AWS → `LoadDefaultConfig` retorna erro; logamos com hint `aws sso login`
- Modelo não habilitado na conta → Bedrock retorna `AccessDeniedException`; propagamos a mensagem
- Throttling → erro propagado (sem retry custom; AWS SDK já tem retries default)

## Out of scope

- Migração de dados do `memories` antigo
- Suporte multi-provider
- Mudanças em prompts/skills/MCP/observabilidade

## Testes

- `internal/bedrock/translate_test.go`: round-trip de mensagens, tools, finish_reason
- `go build ./...` + `go test ./...` antes do PR

## PR

Branch `feat/aws-bedrock-llm` → master. Título: `feat: substitui OpenRouter por AWS Bedrock como backend LLM`.
