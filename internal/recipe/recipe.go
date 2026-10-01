package recipe

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

// Recipe 是按进水来源与处理阶段维护的加药配方。同一键下只保留一个活跃版本，
// 新版本生效后旧版本立即作废，但旧版本记录仍然保留，历史建议不会被改写。
type Recipe struct {
	ID           uuid.UUID          `json:"id"`
	Source       string             `json:"source"`
	Stage        model.ProcessStage `json:"stage"`
	Chemical     string             `json:"chemical"`
	Version      uint64             `json:"version"`
	DosePerCOD   float64            `json:"dose_per_cod"`
	UnitCOD      string             `json:"unit_cod"`
	UnitRate     string             `json:"unit_rate"`
	MinRate      float64            `json:"min_rate"`
	MaxRate      float64            `json:"max_rate"`
	SampleMaxAge time.Duration      `json:"sample_max_age"`
	Note         string             `json:"note,omitempty"`
	Active       bool               `json:"active"`
	CreatedAt    time.Time          `json:"created_at"`
	SupersededAt *time.Time         `json:"superseded_at,omitempty"`
}

// Draft 是新建配方版本时由操作员提交的参数。
type Draft struct {
	Source       string
	Stage        model.ProcessStage
	Chemical     string
	DosePerCOD   float64
	SampleMaxAge time.Duration
	MinRate      float64
	MaxRate      float64
	Note         string
}

// Key 唯一标识一条配方线（进水来源 + 处理阶段）。
type Key struct {
	Source string
	Stage  model.ProcessStage
}

func normalizeSource(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// NewKey 校验并构造配方键。
func NewKey(source string, stage model.ProcessStage) (Key, error) {
	key := Key{Source: normalizeSource(source), Stage: stage}
	if key.Source == "" {
		return Key{}, errors.New("recipe source is required")
	}
	if len(key.Source) > 64 {
		return Key{}, errors.New("recipe source is too long")
	}
	if !supportedStage(stage) {
		return Key{}, errors.New("recipe stage is unsupported")
	}
	return key, nil
}

func supportedStage(stage model.ProcessStage) bool {
	switch stage {
	case model.StageReceiving, model.StageEqualizing, model.StageBiological,
		model.StageSettling, model.StageQualified, model.StageDischarging:
		return true
	default:
		return false
	}
}

// Book 保存所有配方版本，键为进水来源 + 处理阶段。
type Book struct {
	mu      sync.RWMutex
	entries map[Key][]Recipe
}

func NewBook() *Book {
	return &Book{entries: make(map[Key][]Recipe)}
}

// Publish 创建新版本并自动作废旧的活跃版本。配方更新只影响后续建议，
// 已经生成的建议仍指向其创建时的版本。
func (b *Book) Publish(draft Draft, now time.Time) (Recipe, error) {
	key, err := NewKey(draft.Source, draft.Stage)
	if err != nil {
		return Recipe{}, err
	}
	chemical := strings.TrimSpace(draft.Chemical)
	if chemical == "" {
		return Recipe{}, errors.New("recipe chemical is required")
	}
	if draft.DosePerCOD <= 0 {
		return Recipe{}, errors.New("recipe dose per COD must be positive")
	}
	if draft.SampleMaxAge <= 0 {
		return Recipe{}, errors.New("recipe sample max age must be positive")
	}
	if draft.MinRate < 0 || draft.MaxRate <= 0 || draft.MinRate > draft.MaxRate {
		return Recipe{}, errors.New("recipe rate range is invalid")
	}
	stamp := now.UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	versions := b.entries[key]
	version := uint64(1)
	if len(versions) > 0 {
		version = versions[len(versions)-1].Version + 1
		if versions[len(versions)-1].Active {
			versions[len(versions)-1].Active = false
			superseded := stamp
			versions[len(versions)-1].SupersededAt = &superseded
		}
	}
	recipe := Recipe{
		ID: uuid.New(), Source: key.Source, Stage: key.Stage, Chemical: chemical,
		Version: version, DosePerCOD: draft.DosePerCOD, UnitCOD: "mg/L", UnitRate: "L/h",
		MinRate: draft.MinRate, MaxRate: draft.MaxRate, SampleMaxAge: draft.SampleMaxAge,
		Note: strings.TrimSpace(draft.Note), Active: true, CreatedAt: stamp,
	}
	b.entries[key] = append(versions, recipe)
	return recipe, nil
}

// Active 返回指定键当前有效的配方版本。
func (b *Book) Active(key Key) (Recipe, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, recipe := range b.entries[key] {
		if recipe.Active {
			return recipe, true
		}
	}
	return Recipe{}, false
}

// Get 按 ID 查找任意版本（含已作废版本）。
func (b *Book) Get(id uuid.UUID) (Recipe, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, versions := range b.entries {
		for index := len(versions) - 1; index >= 0; index-- {
			if versions[index].ID == id {
				return versions[index], true
			}
		}
	}
	return Recipe{}, false
}

// Versions 返回指定键的全部版本，按版本号升序。
func (b *Book) Versions(key Key) []Recipe {
	b.mu.RLock()
	defer b.mu.RUnlock()
	versions := b.entries[key]
	return append([]Recipe(nil), versions...)
}

// ActiveRecipes 列出当前所有有效配方版本。
func (b *Book) ActiveRecipes() []Recipe {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Recipe, 0)
	for _, versions := range b.entries {
		for _, recipe := range versions {
			if recipe.Active {
				result = append(result, recipe)
			}
		}
	}
	return result
}
