package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema SQL necessário:
//
//	CREATE TABLE IF NOT EXISTS episodes (
//	    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
//	    session_id  TEXT NOT NULL,
//	    user_id     TEXT,
//	    goal        TEXT NOT NULL,
//	    outcome     TEXT NOT NULL CHECK (outcome IN ('success','failure','partial')),
//	    summary     TEXT,
//	    tools_used  JSONB DEFAULT '[]',
//	    duration_ms BIGINT,
//	    created_at  TIMESTAMPTZ DEFAULT NOW()
//	);
//	CREATE INDEX IF NOT EXISTS episodes_session_idx ON episodes (session_id);
//	CREATE INDEX IF NOT EXISTS episodes_outcome_idx ON episodes (outcome);
//	CREATE INDEX IF NOT EXISTS episodes_goal_fts_idx ON episodes USING gin(to_tsvector('portuguese', goal));

type Episode struct {
	ID         string
	SessionID  string
	UserID     string
	Goal       string
	Outcome    string // "success" | "failure" | "partial"
	Summary    string
	ToolsUsed  []string
	DurationMs int64
	CreatedAt  time.Time
}

// EpisodicMemory persiste o histórico de execuções do agente.
// Permite aprender com o passado e auditar comportamento.
type EpisodicMemory struct {
	db *pgxpool.Pool
}

func NewEpisodicMemory(db *pgxpool.Pool) *EpisodicMemory {
	return &EpisodicMemory{db: db}
}

// Record salva um episódio concluído.
func (e *EpisodicMemory) Record(ctx context.Context, ep Episode) error {
	toolsJSON, _ := json.Marshal(ep.ToolsUsed)

	_, err := e.db.Exec(ctx, `
		INSERT INTO episodes
			(session_id, user_id, goal, outcome, summary, tools_used, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, ep.SessionID, ep.UserID, sanitizeUTF8(ep.Goal), ep.Outcome, sanitizeUTF8(ep.Summary), toolsJSON, ep.DurationMs)

	if err != nil {
		return fmt.Errorf("episodic_memory: record failed: %w", err)
	}
	return nil
}

// FindSimilar recupera episódios passados com objetivos parecidos.
// Usa full-text search para encontrar runs relacionados.
func (e *EpisodicMemory) FindSimilar(ctx context.Context, goal string, limit int) ([]Episode, error) {
	if limit == 0 {
		limit = 3
	}

	rows, err := e.db.Query(ctx, `
		SELECT id, session_id, goal, outcome, summary, tools_used, duration_ms, created_at
		FROM episodes
		WHERE outcome = 'success'
		  AND to_tsvector('portuguese', goal) @@ plainto_tsquery('portuguese', $1)
		ORDER BY created_at DESC
		LIMIT $2
	`, goal, limit)
	if err != nil {
		return nil, fmt.Errorf("episodic_memory: find similar failed: %w", err)
	}
	defer rows.Close()

	var episodes []Episode
	for rows.Next() {
		var ep Episode
		var toolsRaw []byte
		if err := rows.Scan(
			&ep.ID, &ep.SessionID, &ep.Goal, &ep.Outcome,
			&ep.Summary, &toolsRaw, &ep.DurationMs, &ep.CreatedAt,
		); err != nil {
			continue
		}
		json.Unmarshal(toolsRaw, &ep.ToolsUsed)
		episodes = append(episodes, ep)
	}

	return episodes, rows.Err()
}

// GetBySession retorna todos os episódios de uma sessão.
func (e *EpisodicMemory) GetBySession(ctx context.Context, sessionID string) ([]Episode, error) {
	rows, err := e.db.Query(ctx, `
		SELECT id, goal, outcome, summary, tools_used, duration_ms, created_at
		FROM episodes
		WHERE session_id = $1
		ORDER BY created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var episodes []Episode
	for rows.Next() {
		var ep Episode
		var toolsRaw []byte
		rows.Scan(&ep.ID, &ep.Goal, &ep.Outcome, &ep.Summary, &toolsRaw, &ep.DurationMs, &ep.CreatedAt)
		json.Unmarshal(toolsRaw, &ep.ToolsUsed)
		episodes = append(episodes, ep)
	}

	return episodes, nil
}

// Stats retorna métricas agregadas sobre as execuções.
func (e *EpisodicMemory) Stats(ctx context.Context) (map[string]any, error) {
	var total, success, failure int
	var avgDuration float64

	err := e.db.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE outcome = 'success'),
			COUNT(*) FILTER (WHERE outcome = 'failure'),
			AVG(duration_ms)
		FROM episodes
	`).Scan(&total, &success, &failure, &avgDuration)

	if err != nil {
		return nil, err
	}

	return map[string]any{
		"total":           total,
		"success":         success,
		"failure":         failure,
		"success_rate":    float64(success) / float64(total),
		"avg_duration_ms": avgDuration,
	}, nil
}

// sanitizeUTF8 remove bytes inválidos para evitar erro 22021 no Postgres.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "")
}

// CreateSchema cria as tabelas e índices necessários.
func (e *EpisodicMemory) CreateSchema(ctx context.Context) error {
	_, err := e.db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS episodes (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			session_id  TEXT NOT NULL,
			user_id     TEXT,
			goal        TEXT NOT NULL,
			outcome     TEXT NOT NULL CHECK (outcome IN ('success','failure','partial')),
			summary     TEXT,
			tools_used  JSONB DEFAULT '[]',
			duration_ms BIGINT,
			created_at  TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS episodes_session_idx
			ON episodes (session_id);
		CREATE INDEX IF NOT EXISTS episodes_goal_fts_idx
			ON episodes USING gin(to_tsvector('portuguese', goal));
	`)
	return err
}
