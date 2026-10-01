package recipe

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusRejected  Status = "rejected"
)

// Basis snapshots every input a recommendation was computed from. The snapshot
// is part of the immutable record: later recipe versions never rewrite it.
type Basis struct {
	Source     string             `json:"source"`
	Stage      model.ProcessStage `json:"stage"`
	FlowM3Hour float64            `json:"flow_m3_hour"`
	CODMgL     float64            `json:"cod_mg_l"`
	DoseRatio  float64            `json:"dose_ratio"`
}

// Recommendation is an immutable dosing suggestion. Only its lifecycle status
// may change (pending -> confirmed/rejected); the suggested rate and its basis
// are fixed at creation.
type Recommendation struct {
	ID            uuid.UUID    `json:"id"`
	LineID        model.LineID `json:"line_id"`
	BatchID       uuid.UUID    `json:"batch_id"`
	RecipeCode    string       `json:"recipe_code"`
	RecipeVersion uint64       `json:"recipe_version"`
	SampleID      uuid.UUID    `json:"sample_id"`
	SampledAt     time.Time    `json:"sampled_at"`
	Chemical      string       `json:"chemical"`
	Rate          float64      `json:"rate"`
	Basis         Basis        `json:"basis"`
	Status        Status       `json:"status"`
	CreatedAt     time.Time    `json:"created_at"`
	DecidedAt     *time.Time   `json:"decided_at,omitempty"`
	CommandID     *uuid.UUID   `json:"command_id,omitempty"`
}

type Book struct {
	mu    sync.RWMutex
	byID  map[uuid.UUID]Recommendation
	order []uuid.UUID
}

func NewBook() *Book {
	return &Book{byID: make(map[uuid.UUID]Recommendation)}
}

func (b *Book) Add(recommendation Recommendation) error {
	if recommendation.ID == uuid.Nil || recommendation.LineID == "" || recommendation.BatchID == uuid.Nil {
		return errors.New("recommendation identity is incomplete")
	}
	if recommendation.Status != StatusPending {
		return errors.New("recommendation must start pending")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.byID[recommendation.ID]; exists {
		return errors.New("recommendation already exists")
	}
	b.byID[recommendation.ID] = recommendation
	b.order = append(b.order, recommendation.ID)
	return nil
}

func (b *Book) Get(id uuid.UUID) (Recommendation, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	recommendation, ok := b.byID[id]
	return recommendation, ok
}

// Decide moves a pending recommendation to its final status. The suggested
// rate, basis and recipe reference are never modified.
func (b *Book) Decide(id uuid.UUID, status Status, commandID *uuid.UUID, now time.Time) (Recommendation, error) {
	if status != StatusConfirmed && status != StatusRejected {
		return Recommendation{}, errors.New("recommendation decision is unsupported")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	recommendation, ok := b.byID[id]
	if !ok {
		return Recommendation{}, errors.New("recommendation is not found")
	}
	if recommendation.Status != StatusPending {
		return Recommendation{}, errors.New("recommendation is already decided")
	}
	stamp := now.UTC()
	recommendation.Status = status
	recommendation.DecidedAt = &stamp
	recommendation.CommandID = commandID
	b.byID[id] = recommendation
	return recommendation, nil
}

// ForLine returns the line's recommendations newest first. Insertion order is
// used so the result is deterministic even when timestamps tie.
func (b *Book) ForLine(lineID model.LineID) []Recommendation {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Recommendation, 0, len(b.order))
	for i := len(b.order) - 1; i >= 0; i-- {
		if recommendation := b.byID[b.order[i]]; recommendation.LineID == lineID {
			result = append(result, recommendation)
		}
	}
	return result
}
