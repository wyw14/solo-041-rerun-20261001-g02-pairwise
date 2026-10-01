package recipe

import (
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/intake"
	"github.com/wyw14/cry-97/internal/model"
)

// DefaultMaxSampleAge bounds how old a COD online sample may be before it is
// no longer trusted for dosing decisions.
const DefaultMaxSampleAge = 30 * time.Minute

type Input struct {
	Line         model.LineState
	Batch        *model.Batch
	Admission    *intake.Admission
	Sample       *model.Sample
	Recipes      *Store
	Now          time.Time
	MaxSampleAge time.Duration
}

// Outcome is the result of one evaluation. When OK is false, Reason explains
// why no trustworthy suggestion exists and Recommendation is nil; nothing is
// commanded in that case.
type Outcome struct {
	OK             bool            `json:"ok"`
	Reason         string          `json:"reason,omitempty"`
	Recommendation *Recommendation `json:"recommendation,omitempty"`
}

// Evaluate builds a dosing recommendation from the current line context. It is
// pure: every input is passed in and no command is issued as a side effect.
func Evaluate(input Input) Outcome {
	if input.Line.Emergency {
		return blocked("process line is under emergency isolation")
	}
	if input.Batch == nil || !input.Batch.IsActive() {
		return blocked("process line has no active batch")
	}
	batch := *input.Batch
	if input.Admission == nil {
		return blocked("process line has no intake admission")
	}
	admission := *input.Admission
	if input.Sample == nil {
		return blocked("no cod sample observed for process line")
	}
	sample := *input.Sample
	if sample.Kind != model.SampleCOD {
		return blocked("no cod sample observed for process line")
	}
	if !sample.Finite() || sample.Value < 0 {
		return blocked("latest cod sample is not a valid measurement")
	}
	if sample.ObservedAt.Before(batch.CreatedAt) {
		return blocked("latest cod sample predates the active batch")
	}
	maxAge := input.MaxSampleAge
	if maxAge <= 0 {
		maxAge = DefaultMaxSampleAge
	}
	if input.Now.Sub(sample.ObservedAt) > maxAge {
		return blocked("latest cod sample is stale")
	}
	matched, found := input.Recipes.Match(admission.Source, batch.Stage, admission.FlowM3Hour)
	if !found {
		return blocked("no recipe matches the current source, stage and flow")
	}
	recommendation := Recommendation{
		ID: uuid.New(), LineID: batch.LineID, BatchID: batch.ID,
		RecipeCode: matched.Code, RecipeVersion: matched.Version,
		SampleID: sample.ID, SampledAt: sample.ObservedAt,
		Chemical: matched.Chemical, Rate: matched.Rate(sample.Value, admission.FlowM3Hour),
		Basis: Basis{
			Source: admission.Source, Stage: batch.Stage, FlowM3Hour: admission.FlowM3Hour,
			CODMgL: sample.Value, DoseRatio: matched.DoseRatio,
		},
		Status: StatusPending, CreatedAt: input.Now.UTC(),
	}
	return Outcome{OK: true, Recommendation: &recommendation}
}

func blocked(reason string) Outcome {
	return Outcome{OK: false, Reason: reason}
}
