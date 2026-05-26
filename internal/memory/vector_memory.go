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

// VectorMemory persiste e busca memórias por similaridade semântica via pgvector.
type VectorMemory struct {
	db         *pgxpool.Pool
	llm        *bedrock.Client
	embedModel string
}

func NewVectorMemory(db *pgxpool.Pool, llm *bedrock.Client, embedModel string) *VectorMemory {
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
	metaJSON, _ := json.Marshal(metadata)
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
			continue
		}
		results = append(results, content)
	}
	return results, rows.Err()
}

// CreateSchema (re)cria a tabela de memórias vetoriais. A dimensão mudou de
// 1536 (OpenAI text-embedding-3-small) para 1024 (Amazon Titan embed v2),
// então dropamos a tabela antiga — vetores anteriores não são compatíveis.
// EpisodicMemory preserva o histórico de runs.
func (v *VectorMemory) CreateSchema(ctx context.Context) error {
	_, err := v.db.Exec(ctx, `
		CREATE EXTENSION IF NOT EXISTS vector;
		DROP TABLE IF EXISTS memories;
		CREATE TABLE memories (
			id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			content    TEXT NOT NULL,
			embedding  VECTOR(1024),
			metadata   JSONB DEFAULT '{}',
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS memories_embedding_idx
			ON memories USING hnsw (embedding vector_cosine_ops);
	`)
	return err
}
