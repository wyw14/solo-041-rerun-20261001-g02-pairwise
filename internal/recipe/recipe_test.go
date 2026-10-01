package recipe

import (
	"math"
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/intake"
	"github.com/wyw14/cry-97/internal/model"
)

var stamp = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func draft(code string, ratio float64) Draft {
	return Draft{
		Code: code, Source: "north-intake", Stage: model.StageReceiving,
		MinFlow: 100, MaxFlow: 1000, Chemical: "carbon", DoseRatio: ratio,
	}
}

func TestStoreVersionsAreAppendOnly(t *testing.T) {
	store := NewStore()
	first, err := store.Register(draft("carbon-north", 0.8), stamp)
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}
	second, err := store.Register(draft("carbon-north", 1.2), stamp.Add(time.Minute))
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if first.Version != 1 || second.Version != 2 {
		t.Fatalf("expected sequential versions, got %d and %d", first.Version, second.Version)
	}
	versions := store.Versions("carbon-north")
	if len(versions) != 2 || versions[0].DoseRatio != 0.8 || versions[1].DoseRatio != 1.2 {
		t.Fatalf("older versions must be preserved, got %+v", versions)
	}
	current, ok := store.Current("carbon-north")
	if !ok || current.DoseRatio != 1.2 {
		t.Fatalf("current should be the latest version, got %+v", current)
	}
}

func TestStoreRegisterValidatesDraft(t *testing.T) {
	store := NewStore()
	invalid := []Draft{
		{Code: "", Source: "s", Stage: model.StageReceiving, MinFlow: 1, MaxFlow: 2, Chemical: "c", DoseRatio: 1},
		{Code: "c", Source: "", Stage: model.StageReceiving, MinFlow: 1, MaxFlow: 2, Chemical: "c", DoseRatio: 1},
		{Code: "c", Source: "s", Stage: "", MinFlow: 1, MaxFlow: 2, Chemical: "c", DoseRatio: 1},
		{Code: "c", Source: "s", Stage: model.StageReceiving, MinFlow: 1, MaxFlow: 2, Chemical: "", DoseRatio: 1},
		{Code: "c", Source: "s", Stage: model.StageReceiving, MinFlow: 0, MaxFlow: 2, Chemical: "c", DoseRatio: 1},
		{Code: "c", Source: "s", Stage: model.StageReceiving, MinFlow: 5, MaxFlow: 2, Chemical: "c", DoseRatio: 1},
		{Code: "c", Source: "s", Stage: model.StageReceiving, MinFlow: 1, MaxFlow: 2, Chemical: "c", DoseRatio: 0},
	}
	for index, value := range invalid {
		if _, err := store.Register(value, stamp); err == nil {
			t.Fatalf("draft %d should be rejected", index)
		}
	}
}

func TestStoreMatchUsesLatestVersion(t *testing.T) {
	store := NewStore()
	if _, err := store.Register(draft("carbon-north", 0.8), stamp); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	matched, ok := store.Match("north-intake", model.StageReceiving, 500)
	if !ok || matched.Version != 1 {
		t.Fatalf("expected v1 to match, got %+v", matched)
	}
	narrower := draft("carbon-north", 1.2)
	narrower.MaxFlow = 300
	if _, err := store.Register(narrower, stamp.Add(time.Minute)); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if _, ok := store.Match("north-intake", model.StageReceiving, 500); ok {
		t.Fatal("latest version no longer covers 500 m3/h, match must fail")
	}
	matched, ok = store.Match("north-intake", model.StageReceiving, 200)
	if !ok || matched.Version != 2 || matched.DoseRatio != 1.2 {
		t.Fatalf("expected v2 to match inside the narrower band, got %+v", matched)
	}
	if _, ok := store.Match("other-source", model.StageReceiving, 200); ok {
		t.Fatal("different source must not match")
	}
}

type evaluateCase struct {
	name   string
	mutate func(*Input)
}

func evaluateCases() []evaluateCase {
	return []evaluateCase{
		{"emergency line", func(in *Input) { in.Line.Emergency = true }},
		{"no batch", func(in *Input) { in.Batch = nil }},
		{"closed batch", func(in *Input) {
			batch := *in.Batch
			batch.Stage = model.StageClosed
			in.Batch = &batch
		}},
		{"no admission", func(in *Input) { in.Admission = nil }},
		{"no sample", func(in *Input) { in.Sample = nil }},
		{"non cod sample", func(in *Input) {
			sample := *in.Sample
			sample.Kind = model.SampleDissolvedOxygen
			in.Sample = &sample
		}},
		{"nan sample", func(in *Input) {
			sample := *in.Sample
			sample.Value = math.NaN()
			in.Sample = &sample
		}},
		{"negative sample", func(in *Input) {
			sample := *in.Sample
			sample.Value = -5
			in.Sample = &sample
		}},
		{"sample predates batch", func(in *Input) {
			sample := *in.Sample
			sample.ObservedAt = in.Batch.CreatedAt.Add(-time.Minute)
			in.Sample = &sample
		}},
		{"stale sample", func(in *Input) {
			batch := *in.Batch
			batch.CreatedAt = in.Now.Add(-3 * time.Hour)
			in.Batch = &batch
			sample := *in.Sample
			sample.ObservedAt = in.Now.Add(-2 * time.Hour)
			in.Sample = &sample
		}},
		{"no matching recipe", func(in *Input) { in.Recipes = NewStore() }},
	}
}

func baseInput() Input {
	store := NewStore()
	if _, err := store.Register(draft("carbon-north", 0.8), stamp); err != nil {
		panic(err)
	}
	batch := model.NewBatch("line-1", stamp)
	admission := intake.Admission{LineID: "line-1", FlowM3Hour: 500, Source: "north-intake", AcceptedAt: stamp}
	sample := model.NewSample("line-1", "basin-line-1", model.SampleCOD, 1, 120, "mg/L", stamp.Add(time.Minute))
	return Input{
		Line: model.NewLineState("line-1", stamp), Batch: &batch, Admission: &admission,
		Sample: &sample, Recipes: store, Now: stamp.Add(2 * time.Minute), MaxSampleAge: DefaultMaxSampleAge,
	}
}

func TestEvaluateBlockedOutcomes(t *testing.T) {
	for _, tc := range evaluateCases() {
		t.Run(tc.name, func(t *testing.T) {
			input := baseInput()
			tc.mutate(&input)
			outcome := Evaluate(input)
			if outcome.OK {
				t.Fatalf("expected blocked outcome, got %+v", outcome.Recommendation)
			}
			if outcome.Reason == "" {
				t.Fatal("blocked outcome must explain the reason")
			}
			if outcome.Recommendation != nil {
				t.Fatal("blocked outcome must not carry a recommendation")
			}
		})
	}
}

func TestEvaluateComputesRecommendation(t *testing.T) {
	input := baseInput()
	outcome := Evaluate(input)
	if !outcome.OK {
		t.Fatalf("expected ok outcome, got reason %q", outcome.Reason)
	}
	recommendation := outcome.Recommendation
	if recommendation.Rate != 48 {
		t.Fatalf("rate = cod 120 * flow 500 * ratio 0.8 / 1000 = 48, got %v", recommendation.Rate)
	}
	if recommendation.Status != StatusPending {
		t.Fatalf("recommendation must start pending, got %s", recommendation.Status)
	}
	if recommendation.BatchID != input.Batch.ID || recommendation.SampleID != input.Sample.ID {
		t.Fatal("recommendation must reference the active batch and the sampled COD reading")
	}
	if recommendation.RecipeCode != "carbon-north" || recommendation.RecipeVersion != 1 {
		t.Fatalf("recommendation must pin the recipe version, got %s@v%d", recommendation.RecipeCode, recommendation.RecipeVersion)
	}
	basis := recommendation.Basis
	if basis.CODMgL != 120 || basis.FlowM3Hour != 500 || basis.DoseRatio != 0.8 ||
		basis.Source != "north-intake" || basis.Stage != model.StageReceiving {
		t.Fatalf("basis must snapshot every input, got %+v", basis)
	}
}

func TestBookLifecycle(t *testing.T) {
	book := NewBook()
	outcome := Evaluate(baseInput())
	if !outcome.OK {
		t.Fatal("expected ok outcome")
	}
	recommendation := *outcome.Recommendation
	if err := book.Add(recommendation); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := book.Add(recommendation); err == nil {
		t.Fatal("duplicate add must fail")
	}
	recommendation.Status = StatusConfirmed
	if err := book.Add(recommendation); err == nil {
		t.Fatal("book must only accept pending recommendations")
	}
	if _, err := book.Decide(recommendation.ID, StatusPending, nil, stamp); err == nil {
		t.Fatal("decision must be confirm or reject")
	}
	decided, err := book.Decide(recommendation.ID, StatusRejected, nil, stamp)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if decided.Status != StatusRejected || decided.DecidedAt == nil {
		t.Fatalf("expected rejected with decision time, got %+v", decided)
	}
	if decided.Rate != 48 || decided.Basis.DoseRatio != 0.8 {
		t.Fatal("deciding must never rewrite the suggested rate or basis")
	}
	if _, err := book.Decide(recommendation.ID, StatusConfirmed, nil, stamp); err == nil {
		t.Fatal("decided recommendation cannot be decided again")
	}
}

func TestBookForLineOrdersNewestFirst(t *testing.T) {
	book := NewBook()
	first := Evaluate(baseInput())
	second := Evaluate(baseInput())
	second.Recommendation.LineID = "line-2"
	for _, outcome := range []Outcome{first, second} {
		if err := book.Add(*outcome.Recommendation); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	third := Evaluate(baseInput())
	third.Recommendation.CreatedAt = third.Recommendation.CreatedAt.Add(time.Minute)
	if err := book.Add(*third.Recommendation); err != nil {
		t.Fatalf("add third: %v", err)
	}
	lineOne := book.ForLine("line-1")
	if len(lineOne) != 2 || lineOne[0].ID != third.Recommendation.ID {
		t.Fatalf("expected newest first for line-1, got %+v", lineOne)
	}
	if len(book.ForLine("line-2")) != 1 {
		t.Fatal("line-2 should have exactly one recommendation")
	}
}
