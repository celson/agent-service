package agent

import (
	"context"

	"github.com/yourorg/agent-service/internal/bedrock"
	"github.com/yourorg/agent-service/internal/memory"
)

// Interfaces que descrevem a fatia mínima de cada dependência usada pelo Agent.
// Os tipos concretos do pacote bedrock e memory satisfazem essas interfaces
// estruturalmente — main.go continua passando *bedrock.Client, *memory.WorkingMemory,
// etc., sem ajuste. Em testes, fakes locais implementam apenas o que precisam.

// LLMClient é a porção do bedrock.Client que o Agent realmente usa.
type LLMClient interface {
	Chat(ctx context.Context, req bedrock.ChatRequest) (*bedrock.ChatResponse, error)
}

// WorkingMemoryStore persiste estado de sessão (Redis na produção).
type WorkingMemoryStore interface {
	Load(ctx context.Context, sessionID string) (*memory.AgentState, error)
	Checkpoint(ctx context.Context, sessionID, step, result string) error
}

// VectorMemoryStore expõe recall semântico (pgvector na produção).
type VectorMemoryStore interface {
	Search(ctx context.Context, query string, topK int) ([]string, error)
	Store(ctx context.Context, content string, metadata map[string]any) error
}

// EpisodicMemoryStore registra histórico de execuções (Postgres na produção).
type EpisodicMemoryStore interface {
	Record(ctx context.Context, ep memory.Episode) error
	FindSimilar(ctx context.Context, goal string, limit int) ([]memory.Episode, error)
}
