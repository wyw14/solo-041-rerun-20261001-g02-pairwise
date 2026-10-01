package recipe

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

// Recipe is one immutable version of a dosing recipe. Recipes are identified
// by Code; updating a recipe appends a new version and never rewrites older
// ones, so historical recommendations keep the inputs they were built from.
type Recipe struct {
	Code      string             `json:"code"`
	Source    string             `json:"source"`
	Stage     model.ProcessStage `json:"stage"`
	MinFlow   float64            `json:"min_flow"`
	MaxFlow   float64            `json:"max_flow"`
	Chemical  string             `json:"chemical"`
	DoseRatio float64            `json:"dose_ratio"`
	Version   uint64             `json:"version"`
	CreatedAt time.Time          `json:"created_at"`
}

// Draft carries the operator supplied fields for the next version of a recipe.
type Draft struct {
	Code      string             `json:"code"`
	Source    string             `json:"source"`
	Stage     model.ProcessStage `json:"stage"`
	MinFlow   float64            `json:"min_flow"`
	MaxFlow   float64            `json:"max_flow"`
	Chemical  string             `json:"chemical"`
	DoseRatio float64            `json:"dose_ratio"`
}

func (d Draft) validate() error {
	if d.Code == "" || d.Source == "" || d.Chemical == "" || d.Stage == "" {
		return errors.New("recipe code, source, stage and chemical are required")
	}
	if d.MinFlow <= 0 || d.MaxFlow < d.MinFlow {
		return errors.New("recipe flow band is invalid")
	}
	if d.DoseRatio <= 0 {
		return errors.New("recipe dose ratio must be positive")
	}
	return nil
}

// Covers reports whether this version applies to the given source, stage and flow.
func (r Recipe) Covers(source string, stage model.ProcessStage, flow float64) bool {
	return r.Source == source && r.Stage == stage && flow >= r.MinFlow && flow <= r.MaxFlow
}

// Rate converts a COD reading and intake flow into a dosing setpoint in kg/h.
func (r Recipe) Rate(codMgL float64, flowM3Hour float64) float64 {
	return codMgL * flowM3Hour * r.DoseRatio / 1000
}

type Store struct {
	mu       sync.RWMutex
	versions map[string][]Recipe
}

func NewStore() *Store {
	return &Store{versions: make(map[string][]Recipe)}
}

// Register appends a new version for the draft's code and returns it.
func (s *Store) Register(draft Draft, now time.Time) (Recipe, error) {
	if err := draft.validate(); err != nil {
		return Recipe{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recipe := Recipe{
		Code: draft.Code, Source: draft.Source, Stage: draft.Stage,
		MinFlow: draft.MinFlow, MaxFlow: draft.MaxFlow, Chemical: draft.Chemical,
		DoseRatio: draft.DoseRatio, Version: uint64(len(s.versions[draft.Code]) + 1),
		CreatedAt: now.UTC(),
	}
	s.versions[draft.Code] = append(s.versions[draft.Code], recipe)
	return recipe, nil
}

func (s *Store) Current(code string) (Recipe, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := s.versions[code]
	if len(versions) == 0 {
		return Recipe{}, false
	}
	return versions[len(versions)-1], true
}

func (s *Store) Versions(code string) []Recipe {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Recipe{}, s.versions[code]...)
}

// Latest returns the newest version of every known recipe code.
func (s *Store) Latest() []Recipe {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Recipe, 0, len(s.versions))
	for _, versions := range s.versions {
		result = append(result, versions[len(versions)-1])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result
}

// Match picks the latest-version recipe covering the source, stage and flow.
// When several recipes cover the same point the most recently registered wins.
func (s *Store) Match(source string, stage model.ProcessStage, flow float64) (Recipe, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best Recipe
	found := false
	for _, versions := range s.versions {
		candidate := versions[len(versions)-1]
		if !candidate.Covers(source, stage, flow) {
			continue
		}
		if !found || candidate.CreatedAt.After(best.CreatedAt) ||
			(candidate.CreatedAt.Equal(best.CreatedAt) && candidate.Code > best.Code) {
			best, found = candidate, true
		}
	}
	return best, found
}
