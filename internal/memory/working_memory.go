package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultTTL = 24 * time.Hour

// AgentState representa o estado intermediário de uma execução do agente.
type AgentState struct {
	Goal           string            `json:"goal"`
	Plan           []string          `json:"plan"`
	CompletedSteps []string          `json:"completed_steps"`
	Variables      map[string]string `json:"variables"` // resultados parciais nomeados
	Iteration      int               `json:"iteration"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// WorkingMemory persiste o estado de execução no Redis.
// Permite retomada de tarefas longas após falha ou timeout.
type WorkingMemory struct {
	client *redis.Client
	ttl    time.Duration
}

func NewWorkingMemory(client *redis.Client, ttl time.Duration) *WorkingMemory {
	if ttl == 0 {
		ttl = defaultTTL
	}
	return &WorkingMemory{client: client, ttl: ttl}
}

func (w *WorkingMemory) Save(ctx context.Context, sessionID string, state *AgentState) error {
	state.UpdatedAt = time.Now()
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("working_memory: marshal failed: %w", err)
	}
	return w.client.Set(ctx, stateKey(sessionID), data, w.ttl).Err()
}

func (w *WorkingMemory) Load(ctx context.Context, sessionID string) (*AgentState, error) {
	data, err := w.client.Get(ctx, stateKey(sessionID)).Bytes()
	if errors.Is(err, redis.Nil) {
		// Sessão nova: retorna estado vazio
		return &AgentState{Variables: make(map[string]string)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("working_memory: get failed: %w", err)
	}

	var state AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("working_memory: unmarshal failed: %w", err)
	}
	if state.Variables == nil {
		state.Variables = make(map[string]string)
	}
	return &state, nil
}

// Checkpoint salva o progresso após cada tool bem-sucedida.
// Se o agente falhar, a retomada sabe o que já foi feito.
func (w *WorkingMemory) Checkpoint(ctx context.Context, sessionID, step, result string) error {
	state, err := w.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	state.CompletedSteps = append(state.CompletedSteps, step)
	state.Variables[step] = result
	state.Iteration++
	return w.Save(ctx, sessionID, state)
}

func (w *WorkingMemory) SetPlan(ctx context.Context, sessionID string, plan []string) error {
	state, err := w.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	state.Plan = plan
	return w.Save(ctx, sessionID, state)
}

// Clear remove o estado da sessão (após conclusão ou cancelamento).
func (w *WorkingMemory) Clear(ctx context.Context, sessionID string) error {
	return w.client.Del(ctx, stateKey(sessionID)).Err()
}

func stateKey(sessionID string) string {
	return fmt.Sprintf("agent:state:%s", sessionID)
}
