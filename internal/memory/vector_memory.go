package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"github.com/yourorg/agent-service/internal/bedrock"
)

const (
	defaultTopK             = 5
	defaultSimilarityThresh = 0.75
)

// Embedder é a fatia mínima de *bedrock.Client usada para gerar embeddings.
// Como interface, permite fakes em testes sem tocar o AWS SDK.
type Embedder interface {
	EmbedOne(ctx context.Context, model, text string) ([]float32, error)
}

// VectorMemory persiste e busca memórias por similaridade semântica via pgvector.
type VectorMemory struct {
	db         *pgxpool.Pool
	llm        Embedder
	embedModel string
}

func NewVectorMemory(db *pgxpool.Pool, llm Embedder, embedModel string) *VectorMemory {
	if embedModel == "" {
		embedModel = bedrock.DefaultEmbedModel
	}
	return &VectorMemory{db: db, llm: llm, embedModel: embedModel}
}

type MemoryEntry struct {
	ID         string
	Content    string
	Metadata   map[string]any
	Similarity float64
	CreatedAt  time.Time
}

func (v *VectorMemory) Store(ctx context.Context, content string, metadata map[string]any) error {
	if content == "" {
		return nil
	}
	embedding, err := v.llm.EmbedOne(ctx, v.embedModel, content)
	if err != nil {
		return fmt.Errorf("vector_memory: embed failed: %w", err)
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("vector_memory: marshal metadata: %w", err)
	}
	_, err = v.db.Exec(ctx, `
		INSERT INTO memories (content, embedding, metadata)
		VALUES ($1, $2, $3)
	`, content, pgvector.NewVector(embedding), metaJSON)
	return err
}

func (v *VectorMemory) Search(ctx context.Context, query string, topK int) ([]string, error) {
	if topK == 0 {
		topK = defaultTopK
	}
	queryEmb, err := v.llm.EmbedOne(ctx, v.embedModel, query)
	if err != nil {
		return nil, fmt.Errorf("vector_memory: embed query failed: %w", err)
	}
	rows, err := v.db.Query(ctx, `
		SELECT content, 1 - (embedding <=> $1) AS similarity
		FROM memories
		WHERE 1 - (embedding <=> $1) > $2
		ORDER BY embedding <=> $1
		LIMIT $3
	`, pgvector.NewVector(queryEmb), defaultSimilarityThresh, topK)
	if err != nil {
		return nil, fmt.Errorf("vector_memory: search failed: %w", err)
	}
	defer rows.Close()

	results := make([]string, 0, topK)
	for rows.Next() {
		var content string
		var similarity float64
		if err := rows.Scan(&content, &similarity); err != nil {
			return nil, fmt.Errorf("vector_memory: scan: %w", err)
		}
		results = append(results, content)
	}
	return results, rows.Err()
}

// embeddingDims é a dimensão do Amazon Titan embed v2. Se a tabela existente
// tiver outra dimensão (ex.: 1536 do antigo OpenAI text-embedding-3-small),
// ela é recriada — vetores de modelos diferentes não são comparáveis.
const embeddingDims = 1024

// CreateSchema cria a tabela de memórias vetoriais de forma idempotente:
// só dropa a tabela existente se a dimensão do embedding for incompatível.
func (v *VectorMemory) CreateSchema(ctx context.Context) error {
	if _, err := v.db.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return fmt.Errorf("vector_memory: create extension: %w", err)
	}

	var existingDims *int
	err := v.db.QueryRow(ctx, `
		SELECT atttypmod
		FROM pg_attribute
		WHERE attrelid = to_regclass('memories') AND attname = 'embedding'
	`).Scan(&existingDims)
	if err == nil && existingDims != nil && *existingDims != embeddingDims {
		if _, err := v.db.Exec(ctx, `DROP TABLE memories`); err != nil {
			return fmt.Errorf("vector_memory: drop incompatible table (dims %d != %d): %w",
				*existingDims, embeddingDims, err)
		}
	}

	_, err = v.db.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS memories (
			id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			content    TEXT NOT NULL,
			embedding  VECTOR(%d),
			metadata   JSONB DEFAULT '{}',
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS memories_embedding_idx
			ON memories USING hnsw (embedding vector_cosine_ops);
	`, embeddingDims))
	return err
}
