package memory

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestWM monta um WorkingMemory apoiado em miniredis (in-memory Redis).
func newTestWM(t *testing.T) (*WorkingMemory, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewWorkingMemory(client, time.Minute), mr
}

// Regressão do PR #29.
//
// Cenário: um state foi persistido com Variables omitido/nil (ex.: SetPlan ou
// Save direto sem inicializar o map). Ao reler, o json.Unmarshal devolvia
// Variables == nil, e qualquer Checkpoint subsequente sofria:
//
//	panic: assignment to entry in nil map.
func TestLoad_InitializesNilVariablesMap(t *testing.T) {
	wm, mr := newTestWM(t)
	ctx := context.Background()
	sessionID := "sess-nil-map"

	// Simula um state pré-existente no Redis com Variables ausente.
	// (json.Marshal(AgentState{Variables: nil}) produz "variables":null.)
	mr.Set("agent:state:"+sessionID, `{"goal":"foo","plan":null,"completed_steps":null,"variables":null,"iteration":0,"updated_at":"2026-05-27T00:00:00Z"}`)

	state, err := wm.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Variables == nil {
		t.Fatal("Load must initialize nil Variables to empty map to avoid downstream panic")
	}

	// Smoke test: Checkpoint deve funcionar sem panicar.
	if err := wm.Checkpoint(ctx, sessionID, "step1", "result1"); err != nil {
		t.Fatalf("Checkpoint after Load should not error: %v", err)
	}

	// Confirma que o valor foi persistido.
	state2, err := wm.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("Load 2: %v", err)
	}
	if got := state2.Variables["step1"]; got != "result1" {
		t.Errorf("Variables[step1] = %q, want 'result1'", got)
	}
}

// Sessão nova (Redis key inexistente) já era tratada — mas vale fixar com teste
// para garantir que ninguém retire a inicialização do path redis.Nil.
func TestLoad_NewSessionReturnsEmptyStateWithMap(t *testing.T) {
	wm, _ := newTestWM(t)
	ctx := context.Background()

	state, err := wm.Load(ctx, "sess-fresh")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Variables == nil {
		t.Error("new session must have non-nil Variables map")
	}
	if len(state.CompletedSteps) != 0 {
		t.Errorf("new session should have empty CompletedSteps, got %v", state.CompletedSteps)
	}
}
