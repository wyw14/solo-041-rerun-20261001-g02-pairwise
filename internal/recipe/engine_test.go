package recipe

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
)

func testClock() (func() time.Time, func(time.Duration) time.Time) {
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	now := base
	clock := func() time.Time { return now }
	advance := func(d time.Duration) time.Time { now = now.Add(d); return now }
	return clock, advance
}

func newTestBook(t *testing.T) *Book {
	t.Helper()
	return NewBook()
}

func publishTestRecipe(t *testing.T, book *Book, now time.Time, overrides ...func(*Draft)) Recipe {
	t.Helper()
	draft := Draft{
		Source: "north-intake", Stage: model.StageBiological, Chemical: "pac",
		DosePerCOD: 0.05, SampleMaxAge: 30 * time.Minute, MinRate: 1, MaxRate: 500,
	}
	for _, apply := range overrides {
		apply(&draft)
	}
	recipe, err := book.Publish(draft, now)
	if err != nil {
		t.Fatalf("publish recipe: %v", err)
	}
	return recipe
}

func evalFixture(now time.Time) (model.LineState, model.Batch, *model.Sample) {
	line := model.LineState{ID: "line-1", Stage: model.StageBiological, Generation: 1}
	batch := model.NewBatch("line-1", now.Add(-2*time.Hour))
	batch.Stage = model.StageBiological
	sample := model.NewSample("line-1", "basin-line-1", model.SampleCOD, 7, 320, "mg/L", now.Add(-5*time.Minute))
	return line, batch, &sample
}

func TestEvaluateComputesRateFromBatchWindowSample(t *testing.T) {
	clock, _ := testClock()
	now := clock()
	book := newTestBook(t)
	publishTestRecipe(t, book, now.Add(-time.Hour))
	engine, err := NewEngine(book)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	line, batch, sample := evalFixture(now)

	outcome := engine.Evaluate(Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: sample}, now)
	if !outcome.Deliverable {
		t.Fatalf("expected deliverable suggestion, reasons: %v", outcome.Suggestion.Reasons)
	}
	s := outcome.Suggestion
	// 320 mg/L * 500 m3/h * 0.05 / 1000 = 8 L/h
	if s.Rate != 8 || s.RawRate != 8 || s.Clamped != "" {
		t.Fatalf("unexpected rate: raw=%f rate=%f clamped=%q", s.RawRate, s.Rate, s.Clamped)
	}
	if s.Basis == nil || s.Basis.SampleID != sample.ID || s.Basis.AgeSeconds != 300 {
		t.Fatalf("suggestion basis does not reference latest COD sample: %+v", s.Basis)
	}
	if s.RecipeVersion != 1 || s.Chemical != "pac" || s.Status != StatusPending {
		t.Fatalf("unexpected suggestion snapshot: %+v", s)
	}
}

func TestEvaluateClampsRateToRecipeRange(t *testing.T) {
	clock, _ := testClock()
	now := clock()
	book := newTestBook(t)
	publishTestRecipe(t, book, now.Add(-time.Hour), func(d *Draft) { d.MinRate = 20; d.MaxRate = 40 })
	engine, _ := NewEngine(book)
	line, batch, sample := evalFixture(now)

	below := engine.Evaluate(Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 100, Sample: sample}, now)
	if !below.Deliverable || below.Suggestion.Rate != 20 || below.Suggestion.Clamped != "min" {
		t.Fatalf("expected min clamp to 20, got deliverable=%v rate=%f clamped=%q", below.Deliverable, below.Suggestion.Rate, below.Suggestion.Clamped)
	}
	above := engine.Evaluate(Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 3000, Sample: sample}, now)
	if !above.Deliverable || above.Suggestion.Rate != 40 || above.Suggestion.Clamped != "max" {
		t.Fatalf("expected max clamp to 40, got deliverable=%v rate=%f clamped=%q", above.Deliverable, above.Suggestion.Rate, above.Suggestion.Clamped)
	}
}

func TestEvaluateBlockedReasonsWithoutIssuingRate(t *testing.T) {
	clock, advance := testClock()
	now := clock()
	book := newTestBook(t)
	publishTestRecipe(t, book, now.Add(-time.Hour), func(d *Draft) { d.SampleMaxAge = 10 * time.Minute })
	engine, _ := NewEngine(book)
	line, batch, sample := evalFixture(now)

	cases := []struct {
		name    string
		ctx     func() Context
		wantSub string
	}{
		{
			name:    "no sample",
			ctx:     func() Context { return Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500} },
			wantSub: "no COD online sample",
		},
		{
			name: "sample predates active batch",
			ctx: func() Context {
				old := model.NewSample("line-1", "b", model.SampleCOD, 1, 300, "mg/L", batch.CreatedAt.Add(-time.Minute))
				return Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: &old}
			},
			wantSub: "predates active batch",
		},
		{
			name: "sample expired per recipe",
			ctx: func() Context {
				return Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: sample}
			},
			wantSub: "recipe allows at most",
		},
		{
			name: "no active recipe for source and stage",
			ctx: func() Context {
				fresh := model.NewSample("line-1", "b", model.SampleCOD, 8, 300, "mg/L", now.Add(-time.Minute))
				return Context{Line: line, Batch: batch, Source: "south-intake", FlowM3Hour: 500, Sample: &fresh}
			},
			wantSub: "no active recipe",
		},
		{
			name: "missing flow",
			ctx: func() Context {
				return Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 0, Sample: sample}
			},
			wantSub: "intake flow",
		},
		{
			name: "non cod sample",
			ctx: func() Context {
				other := model.NewSample("line-1", "b", model.SampleDissolvedOxygen, 9, 4.2, "mg/L", now.Add(-time.Minute))
				return Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: &other}
			},
			wantSub: "not a COD sample",
		},
	}
	// 默认样本距现在 5 分钟；把配方有效期改为 10 分钟后，将时钟推到样本之后 15 分钟以构造过期场景。
	_ = advance
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evaluateAt := now
			if tc.name == "sample expired per recipe" {
				evaluateAt = now.Add(15 * time.Minute)
			}
			outcome := engine.Evaluate(tc.ctx(), evaluateAt)
			if outcome.Deliverable || outcome.Suggestion.Status != StatusBlocked || outcome.Suggestion.Rate != 0 {
				t.Fatalf("expected blocked zero-rate suggestion, got %+v", outcome.Suggestion)
			}
			if !containsReason(outcome.Suggestion.Reasons, tc.wantSub) {
				t.Fatalf("reasons %v do not contain %q", outcome.Suggestion.Reasons, tc.wantSub)
			}
		})
	}
}

func containsReason(reasons []string, sub string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, sub) {
			return true
		}
	}
	return false
}

func TestRecipeVersionSupersedeLeavesStoredSuggestionUntouched(t *testing.T) {
	clock, advance := testClock()
	now := clock()
	book := newTestBook(t)
	first := publishTestRecipe(t, book, now)
	engine, _ := NewEngine(book)
	line, batch, sample := evalFixture(now)

	outcome := engine.Evaluate(Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: sample}, now)
	if !outcome.Deliverable {
		t.Fatalf("expected suggestion: %v", outcome.Suggestion.Reasons)
	}
	stored := outcome.Suggestion
	originalRate := stored.Rate

	advance(time.Hour)
	later := clock()
	second := publishTestRecipe(t, book, later, func(d *Draft) { d.DosePerCOD = 0.2 })
	if first.ID == second.ID || second.Version != 2 || !second.Active {
		t.Fatalf("expected new active version 2, got %+v", second)
	}
	if got, ok := book.Get(first.ID); !ok || got.Active {
		t.Fatalf("expected previous version inactive, got %+v", got)
	}
	// 已生成的历史建议保持 v1 快照不变。
	if stored.RecipeVersion != 1 || stored.Rate != originalRate {
		t.Fatalf("historical suggestion rewritten: %+v", stored)
	}
	fresh := model.NewSample("line-1", "b", model.SampleCOD, 8, 320, "mg/L", later.Add(-time.Minute))
	next := engine.Evaluate(Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: &fresh}, later)
	if !next.Deliverable || next.Suggestion.RecipeVersion != 2 {
		t.Fatalf("expected new suggestion on version 2, got %+v", next.Suggestion)
	}
	if next.Suggestion.Rate != 32 { // 320*500*0.2/1000
		t.Fatalf("unexpected rate under v2: %f", next.Suggestion.Rate)
	}
}

func TestRegisterLifecycleAndImmutability(t *testing.T) {
	clock, advance := testClock()
	now := clock()
	register := NewRegister()
	line, batch, sample := evalFixture(now)
	book := newTestBook(t)
	publishTestRecipe(t, book, now)
	engine, _ := NewEngine(book)

	first := engine.Evaluate(Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: sample}, now).Suggestion
	register.Save(first)

	advance(time.Minute)
	secondSample := model.NewSample("line-1", "b", model.SampleCOD, 8, 300, "mg/L", clock().Add(-30*time.Second))
	second := engine.Evaluate(Context{Line: line, Batch: batch, Source: "north-intake", FlowM3Hour: 500, Sample: &secondSample}, clock()).Suggestion
	register.Save(second)
	register.MarkSuperseded(batch.ID, second.ID, clock())

	old, ok := register.Current(first.ID)
	if !ok || old.Status != StatusSuperseded {
		t.Fatalf("expected first suggestion superseded, got %+v", old)
	}
	pending, ok := register.PendingForBatch(batch.ID)
	if !ok || pending.ID != second.ID {
		t.Fatalf("expected second suggestion pending, got %+v", pending)
	}

	advance(time.Minute)
	confirmed, err := register.Confirm(second.ID, "alice", clock())
	if err != nil || confirmed.Status != StatusConfirmed || confirmed.ConfirmedBy != "alice" {
		t.Fatalf("confirm failed: %v %+v", err, confirmed)
	}
	if _, err := register.Confirm(second.ID, "alice", clock()); err != ErrSuggestionStale {
		t.Fatalf("expected second confirm to be stale, got %v", err)
	}

	advance(time.Minute)
	failed, err := register.Fail(second.ID, "publisher offline", clock())
	if err != nil || failed.Status != StatusFailed || failed.ConfirmedAt != nil {
		t.Fatalf("expected failed reopenable state, got %v %+v", err, failed)
	}
	pending, ok = register.PendingForBatch(batch.ID)
	if !ok || pending.ID != second.ID || pending.FailReason != "publisher offline" {
		t.Fatalf("expected failed suggestion to be confirmable again, got %+v", pending)
	}

	advance(time.Minute)
	register.Confirm(second.ID, "bob", clock())
	history := register.History(uuid.Nil)
	if len(history) != 2 {
		t.Fatalf("expected two history entries, got %d", len(history))
	}
	if history[0].ID != second.ID {
		t.Fatalf("expected newest suggestion first")
	}
}
