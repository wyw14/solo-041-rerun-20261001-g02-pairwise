package recipe

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

// Context 是计算投加建议所需的现场上下文。
type Context struct {
	Line       model.LineState
	Batch      model.Batch
	Source     string        // 批次接纳时登记的进水来源
	FlowM3Hour float64       // 批次接纳时登记的进水流量
	Sample     *model.Sample // 当前线最近一条 COD 在线样本，可能为 nil
}

// Outcome 是一次建议计算的结果。可信样本缺失等情况下 Deliverable 为 false，
// Reasons 给出全部阻断原因，此时不允许下发任何加药命令。
type Outcome struct {
	Deliverable bool       `json:"deliverable"`
	Suggestion  Suggestion `json:"suggestion"`
}

// Engine 依据配方版本和现场上下文计算投加速率建议。
type Engine struct {
	book *Book
}

func NewEngine(book *Book) (*Engine, error) {
	if book == nil {
		return nil, fmt.Errorf("recipe engine requires a recipe book")
	}
	return &Engine{book: book}, nil
}

// Evaluate 只做计算，不持久化、不下发。返回的建议在阻断时状态为 blocked，
// 在满足条件时状态为 pending，由协调层决定是否登记。
func (e *Engine) Evaluate(ctx Context, now time.Time) Outcome {
	stamp := now.UTC()
	reasons := make([]string, 0, 4)

	key, err := NewKey(ctx.Source, ctx.Batch.Stage)
	if err != nil {
		reasons = append(reasons, err.Error())
	}
	if !ctx.Batch.IsActive() {
		reasons = append(reasons, fmt.Sprintf("batch %s is not active at stage %s", ctx.Batch.ID, ctx.Batch.Stage))
	}
	if ctx.FlowM3Hour <= 0 {
		reasons = append(reasons, "intake flow is missing or non-positive")
	}

	// 样本必须是当前线的 COD 在线样本，采样时间落在活跃批次窗口内，且在配方允许的有效期内。
	var basis *SampleBasis
	if ctx.Sample == nil {
		reasons = append(reasons, "no COD online sample has been received on this line")
	} else {
		switch {
		case ctx.Sample.Kind != model.SampleCOD:
			reasons = append(reasons, fmt.Sprintf("latest sample %s is not a COD sample", ctx.Sample.ID))
		case ctx.Sample.LineID != ctx.Line.ID:
			reasons = append(reasons, fmt.Sprintf("COD sample %s belongs to another line", ctx.Sample.ID))
		case !ctx.Sample.Finite():
			reasons = append(reasons, fmt.Sprintf("COD sample %s value is not finite", ctx.Sample.ID))
		case ctx.Sample.ObservedAt.Before(ctx.Batch.CreatedAt):
			reasons = append(reasons, fmt.Sprintf(
				"latest COD sample taken at %s predates active batch started at %s",
				ctx.Sample.ObservedAt.Format(time.RFC3339), ctx.Batch.CreatedAt.Format(time.RFC3339)))
		case ctx.Sample.ObservedAt.After(stamp):
			reasons = append(reasons, fmt.Sprintf("COD sample %s observed_at is in the future", ctx.Sample.ID))
		}
	}

	active, hasRecipe := e.book.Active(key)
	if key.Source != "" && supportedStage(key.Stage) && !hasRecipe {
		reasons = append(reasons, fmt.Sprintf("no active recipe for source %q at stage %s", key.Source, key.Stage))
	}
	if hasRecipe && ctx.Sample != nil && ctx.Sample.Kind == model.SampleCOD &&
		ctx.Sample.LineID == ctx.Line.ID && ctx.Sample.Finite() &&
		!ctx.Sample.ObservedAt.Before(ctx.Batch.CreatedAt) && !ctx.Sample.ObservedAt.After(stamp) {
		age := stamp.Sub(ctx.Sample.ObservedAt)
		if age > active.SampleMaxAge {
			reasons = append(reasons, fmt.Sprintf(
				"latest COD sample is %s old, recipe allows at most %s",
				age.Round(time.Second), active.SampleMaxAge))
		} else {
			basis = &SampleBasis{
				SampleID: ctx.Sample.ID, Sequence: ctx.Sample.Sequence, Value: ctx.Sample.Value,
				Unit: ctx.Sample.Unit, ObservedAt: ctx.Sample.ObservedAt,
				AgeSeconds: int64(age.Seconds()),
			}
		}
	}

	suggestion := Suggestion{
		ID: uuid.New(), LineID: ctx.Line.ID, BatchID: ctx.Batch.ID,
		Source: key.Source, Stage: ctx.Batch.Stage, FlowM3Hour: ctx.FlowM3Hour,
		Status: StatusBlocked, CreatedAt: stamp, UpdatedAt: stamp, Reasons: reasons,
	}
	if hasRecipe {
		suggestion.RecipeID = active.ID
		suggestion.RecipeVersion = active.Version
		suggestion.Chemical = active.Chemical
	}

	if len(reasons) > 0 {
		return Outcome{Deliverable: false, Suggestion: suggestion}
	}

	// 速率 = COD 质量负荷 × 单位 COD 投加系数，并钳制到配方允许区间。
	raw := basis.Value * ctx.FlowM3Hour * active.DosePerCOD / 1000
	rate := raw
	clamped := ""
	if raw < active.MinRate {
		rate, clamped = active.MinRate, "min"
	} else if raw > active.MaxRate {
		rate, clamped = active.MaxRate, "max"
	}
	suggestion.RawRate = raw
	suggestion.Rate = rate
	suggestion.Clamped = clamped
	suggestion.Basis = basis
	suggestion.Status = StatusPending
	return Outcome{Deliverable: true, Suggestion: suggestion}
}
