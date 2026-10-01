package recipe

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

// SuggestionStatus 描述建议的生命周期。
type SuggestionStatus string

const (
	StatusBlocked    SuggestionStatus = "blocked"    // 前置条件不满足，仅展示原因
	StatusPending    SuggestionStatus = "pending"    // 已生成，等待操作员确认
	StatusConfirmed  SuggestionStatus = "confirmed"  // 操作员已确认并下发加药服务
	StatusFailed     SuggestionStatus = "failed"     // 确认后调用加药服务失败，可重新确认
	StatusSuperseded SuggestionStatus = "superseded" // 同批次出现更新的建议
)

// SampleBasis 记录建议所依据的 COD 在线样本。
type SampleBasis struct {
	SampleID   uuid.UUID `json:"sample_id"`
	Sequence   uint64    `json:"sequence"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	ObservedAt time.Time `json:"observed_at"`
	AgeSeconds int64     `json:"age_seconds"`
}

// Suggestion 是一次投加速率建议。建议一经生成即为不可变快照：
// 配方更新或新样本到达都不会改写历史建议，状态流转以追加新版本的方式记录。
type Suggestion struct {
	ID            uuid.UUID          `json:"id"`
	LineID        model.LineID       `json:"line_id"`
	BatchID       uuid.UUID          `json:"batch_id"`
	Source        string             `json:"source"`
	Stage         model.ProcessStage `json:"stage"`
	FlowM3Hour    float64            `json:"flow_m3_hour"`
	RecipeID      uuid.UUID          `json:"recipe_id"`
	RecipeVersion uint64             `json:"recipe_version"`
	Chemical      string             `json:"chemical"`
	Rate          float64            `json:"rate"`
	RawRate       float64            `json:"raw_rate"`
	Clamped       string             `json:"clamped,omitempty"` // "min" / "max"，空表示未触及限值
	Basis         *SampleBasis       `json:"basis,omitempty"`
	Reasons       []string           `json:"reasons,omitempty"`
	Status        SuggestionStatus   `json:"status"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	ConfirmedAt   *time.Time         `json:"confirmed_at,omitempty"`
	ConfirmedBy   string             `json:"confirmed_by,omitempty"`
	CommandID     *uuid.UUID         `json:"command_id,omitempty"`
	FailReason    string             `json:"fail_reason,omitempty"`
}

func (s Suggestion) actionable() bool {
	return s.Status == StatusPending || s.Status == StatusFailed
}

// Actionable 报告建议当前是否可以由操作员确认下发。
func (s Suggestion) Actionable() bool {
	return s.actionable()
}

// Register 保存全部建议。历史建议只追加、不就地修改。
type Register struct {
	mu          sync.RWMutex
	suggestions map[uuid.UUID][]Suggestion
}

func NewRegister() *Register {
	return &Register{suggestions: make(map[uuid.UUID][]Suggestion)}
}

// Save 追加一版建议记录。
func (r *Register) Save(suggestion Suggestion) {
	r.mu.Lock()
	r.suggestions[suggestion.ID] = append(r.suggestions[suggestion.ID], suggestion)
	r.mu.Unlock()
}

// Current 返回某条建议的最新状态版本。
func (r *Register) Current(id uuid.UUID) (Suggestion, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions := r.suggestions[id]
	if len(versions) == 0 {
		return Suggestion{}, false
	}
	return versions[len(versions)-1], true
}

// PendingForBatch 返回批次当前仍可确认的最新建议（旧建议在新建议生成时已被作废）。
func (r *Register) PendingForBatch(batchID uuid.UUID) (Suggestion, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var latest Suggestion
	found := false
	for _, versions := range r.suggestions {
		current := versions[len(versions)-1]
		if current.BatchID != batchID {
			continue
		}
		if !found || current.CreatedAt.After(latest.CreatedAt) {
			latest, found = current, true
		}
	}
	if found && latest.actionable() {
		return latest, true
	}
	return Suggestion{}, false
}

// MarkSuperseded 将批次内早于 keepID 的可确认建议作废。
func (r *Register) MarkSuperseded(batchID, keepID uuid.UUID, now time.Time) {
	stamp := now.UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, versions := range r.suggestions {
		current := versions[len(versions)-1]
		if current.BatchID == batchID && id != keepID && current.actionable() {
			current.Status = StatusSuperseded
			current.UpdatedAt = stamp
			r.suggestions[id] = append(versions, current)
		}
	}
}

// History 返回建议历史，按生成时间倒序。batchID 为 uuid.Nil 时返回全部批次。
func (r *Register) History(batchID uuid.UUID) []Suggestion {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Suggestion, 0)
	for _, versions := range r.suggestions {
		current := versions[len(versions)-1]
		if batchID != uuid.Nil && current.BatchID != batchID {
			continue
		}
		result = append(result, current)
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0; j-- {
			curr, prev := result[j], result[j-1]
			if curr.CreatedAt.After(prev.CreatedAt) ||
				(curr.CreatedAt.Equal(prev.CreatedAt) && curr.ID.String() > prev.ID.String()) {
				result[j-1], result[j] = curr, prev
			} else {
				break
			}
		}
	}
	return result
}

var (
	ErrSuggestionNotFound      = errors.New("recipe suggestion is not found")
	ErrSuggestionStale         = errors.New("recipe suggestion is no longer confirmable")
	ErrSuggestionBatchMismatch = errors.New("recipe suggestion does not belong to the active batch")
)

// Confirm 校验建议仍可确认，并追加已确认状态。命令真正下发成功后再调用 Complete。
func (r *Register) Confirm(id uuid.UUID, operator string, now time.Time) (Suggestion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions, ok := r.suggestions[id]
	if !ok {
		return Suggestion{}, ErrSuggestionNotFound
	}
	current := versions[len(versions)-1]
	if !current.actionable() {
		return Suggestion{}, ErrSuggestionStale
	}
	current.Status = StatusConfirmed
	current.ConfirmedAt = pointerTime(now.UTC())
	current.ConfirmedBy = operator
	current.UpdatedAt = now.UTC()
	r.suggestions[id] = append(versions, current)
	return current, nil
}

// Complete 记录确认后的设备命令。
func (r *Register) Complete(id uuid.UUID, commandID uuid.UUID, now time.Time) (Suggestion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions, ok := r.suggestions[id]
	if !ok {
		return Suggestion{}, ErrSuggestionNotFound
	}
	current := versions[len(versions)-1]
	if current.Status != StatusConfirmed {
		return Suggestion{}, ErrSuggestionStale
	}
	current.CommandID = &commandID
	current.UpdatedAt = now.UTC()
	r.suggestions[id] = append(versions, current)
	return current, nil
}

// Fail 将下发失败的建议回退为可重新确认的失败状态。
func (r *Register) Fail(id uuid.UUID, reason string, now time.Time) (Suggestion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions, ok := r.suggestions[id]
	if !ok {
		return Suggestion{}, ErrSuggestionNotFound
	}
	current := versions[len(versions)-1]
	if current.Status != StatusConfirmed {
		return Suggestion{}, ErrSuggestionStale
	}
	current.Status = StatusFailed
	current.FailReason = reason
	current.ConfirmedAt = nil
	current.UpdatedAt = now.UTC()
	r.suggestions[id] = append(versions, current)
	return current, nil
}

func pointerTime(value time.Time) *time.Time {
	return &value
}
