package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"github.com/yourorg/agent-service/internal/openrouter"
)

const (
	defaultTopK             = 5
	defaultSimilarityThresh = 0.75
)

// VectorMemory persiste e busca memórias por similaridade semântica via pgvector.
type VectorMemory struct {
	db         *pgxpool.Pool
	or         *openrouter.Client
	embedModel string
}

func NewVectorMemory(db *pgxpool.Pool, or *openrouter.Client, embedModel string) *VectorMemory {
	if embedModel == "" {
		embedModel = openrouter.DefaultEmbedModel
	}
	return &VectorMemory{db: db, or: or, embedModel: embedModel}
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
	embedding, err := v.or.EmbedOne(ctx, v.embedModel, content)
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
	queryEmb, err := v.or.EmbedOne(ctx, v.embedModel, query)
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

	var results []string
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

func (v *VectorMemory) CreateSchema(ctx context.Context) error {
	_, err := v.db.Exec(ctx, `
		CREATE EXTENSION IF NOT EXISTS vector;
		CREATE TABLE IF NOT EXISTS memories (
			id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			content    TEXT NOT NULL,
			embedding  VECTOR(1536),
			metadata   JSONB DEFAULT '{}',
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS memories_embedding_idx
			ON memories USING hnsw (embedding vector_cosine_ops);
	`)
	return err
}
