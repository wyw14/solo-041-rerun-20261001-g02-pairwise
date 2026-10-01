package process

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/recipe"
)

func newTestPlant(t *testing.T) (*Plant, func() time.Time, func(time.Duration) time.Time) {
	t.Helper()
	base := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	current := base
	clock := func() time.Time { return current }
	advance := func(d time.Duration) time.Time { current = current.Add(d); return current }
	plant, err := NewPlant(t.TempDir(), []model.LineID{"line-1"}, clock)
	if err != nil {
		t.Fatalf("new plant: %v", err)
	}
	return plant, clock, advance
}

func TestDosingSuggestionBlockedUntilTrustedSample(t *testing.T) {
	plant, _, _ := newTestPlant(t)
	ctx := context.Background()
	if _, err := plant.StartBatch(ctx, "line-1", 500, "north-intake"); err != nil {
		t.Fatalf("start batch: %v", err)
	}
	if _, err := plant.PublishRecipe(recipe.Draft{
		Source: "north-intake", Stage: model.StageReceiving, Chemical: "pac",
		DosePerCOD: 0.05, SampleMaxAge: 30 * time.Minute, MinRate: 1, MaxRate: 500,
	}); err != nil {
		t.Fatalf("publish recipe: %v", err)
	}

	outcome, err := plant.CreateDosingSuggestion("line-1")
	if err != nil {
		t.Fatalf("create suggestion: %v", err)
	}
	if outcome.Deliverable {
		t.Fatalf("expected blocked outcome without COD sample, got %+v", outcome.Suggestion)
	}
	if len(outcome.Suggestion.Reasons) == 0 {
		t.Fatalf("expected reasons explaining missing trusted sample")
	}
	if len(plant.DosingSuggestionHistory("line-1")) != 0 {
		t.Fatalf("blocked suggestion must not be registered")
	}
	if commands := plant.devices.All(); len(commands) != 0 {
		t.Fatalf("no device command may be issued while blocked, got %d", len(commands))
	}
}

func TestDosingSuggestionConfirmFlowCallsDosingServiceOnce(t *testing.T) {
	plant, clock, advance := newTestPlant(t)
	ctx := context.Background()
	batch, err := plant.StartBatch(ctx, "line-1", 500, "north-intake")
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	advance(30 * time.Minute)
	sample := model.NewSample("line-1", "basin-line-1", model.SampleCOD, 1, 320, "mg/L", clock().Add(-time.Minute))
	if err := plant.ReceiveSample(ctx, sample); err != nil {
		t.Fatalf("receive sample: %v", err)
	}
	published, err := plant.PublishRecipe(recipe.Draft{
		Source: "north-intake", Stage: model.StageReceiving, Chemical: "pac",
		DosePerCOD: 0.05, SampleMaxAge: 30 * time.Minute, MinRate: 1, MaxRate: 500,
	})
	if err != nil {
		t.Fatalf("publish recipe: %v", err)
	}

	outcome, err := plant.CreateDosingSuggestion("line-1")
	if err != nil {
		t.Fatalf("create suggestion: %v", err)
	}
	if !outcome.Deliverable {
		t.Fatalf("expected deliverable suggestion, reasons: %v", outcome.Suggestion.Reasons)
	}
	suggestion := outcome.Suggestion
	if suggestion.RecipeID != published.ID || suggestion.Rate != 8 {
		t.Fatalf("unexpected suggestion: %+v", suggestion)
	}

	// 未经操作员确认时，原有手工加药之外没有任何命令由配方模块产生。
	if commands := plant.devices.All(); len(commands) != 0 {
		t.Fatalf("expected no command before operator confirmation, got %d", len(commands))
	}

	confirmed, command, err := plant.ConfirmDosingSuggestion(ctx, suggestion.ID, "alice")
	if err != nil {
		t.Fatalf("confirm suggestion: %v", err)
	}
	if confirmed.Status != recipe.StatusConfirmed || confirmed.CommandID == nil || *confirmed.CommandID != command.ID {
		t.Fatalf("unexpected confirmed state: %+v", confirmed)
	}
	if command.Setpoint != 8 || command.BatchID != batch.ID || command.DeviceID != "doser-pac" {
		t.Fatalf("unexpected dosing command: %+v", command)
	}

	// 建议只能确认一次。
	if _, _, err := plant.ConfirmDosingSuggestion(ctx, suggestion.ID, "alice"); !errors.Is(err, recipe.ErrSuggestionStale) {
		t.Fatalf("expected stale error on second confirm, got %v", err)
	}
}

func TestRecipeUpdateDoesNotRewriteHistoricalSuggestion(t *testing.T) {
	plant, clock, advance := newTestPlant(t)
	ctx := context.Background()
	if _, err := plant.StartBatch(ctx, "line-1", 500, "north-intake"); err != nil {
		t.Fatalf("start batch: %v", err)
	}
	advance(10 * time.Minute)
	sample := model.NewSample("line-1", "basin-line-1", model.SampleCOD, 1, 320, "mg/L", clock().Add(-time.Minute))
	if err := plant.ReceiveSample(ctx, sample); err != nil {
		t.Fatalf("receive sample: %v", err)
	}
	if _, err := plant.PublishRecipe(recipe.Draft{
		Source: "north-intake", Stage: model.StageReceiving, Chemical: "pac",
		DosePerCOD: 0.05, SampleMaxAge: 30 * time.Minute, MinRate: 1, MaxRate: 500,
	}); err != nil {
		t.Fatalf("publish recipe: %v", err)
	}
	outcome, err := plant.CreateDosingSuggestion("line-1")
	if err != nil {
		t.Fatalf("create suggestion: %v", err)
	}
	historical := outcome.Suggestion
	if _, _, err := plant.ConfirmDosingSuggestion(ctx, historical.ID, "alice"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	advance(time.Hour)
	if _, err := plant.PublishRecipe(recipe.Draft{
		Source: "north-intake", Stage: model.StageReceiving, Chemical: "pac",
		DosePerCOD: 0.2, SampleMaxAge: 30 * time.Minute, MinRate: 1, MaxRate: 500,
	}); err != nil {
		t.Fatalf("publish recipe v2: %v", err)
	}
	stored, ok := plant.Suggestion(historical.ID)
	if !ok {
		t.Fatalf("historical suggestion disappeared")
	}
	if stored.RecipeVersion != 1 || stored.Rate != 8 || stored.Status != recipe.StatusConfirmed {
		t.Fatalf("historical suggestion was rewritten by recipe update: %+v", stored)
	}

	// 新建议采用新版本。
	fresh := model.NewSample("line-1", "basin-line-1", model.SampleCOD, 2, 320, "mg/L", clock().Add(-time.Minute))
	if err := plant.ReceiveSample(ctx, fresh); err != nil {
		t.Fatalf("receive fresh sample: %v", err)
	}
	next, err := plant.CreateDosingSuggestion("line-1")
	if err != nil {
		t.Fatalf("create next suggestion: %v", err)
	}
	if !next.Deliverable || next.Suggestion.RecipeVersion != 2 || next.Suggestion.Rate != 32 {
		t.Fatalf("expected version 2 suggestion, got %+v", next.Suggestion)
	}
	stale, ok := plant.Suggestion(historical.ID)
	if !ok || stale.Status != recipe.StatusConfirmed {
		t.Fatalf("confirmed historical suggestion must not be superseded: %+v", stale)
	}
}

func TestSuggestionRejectedWhenBatchNoLongerActive(t *testing.T) {
	plant, clock, _ := newTestPlant(t)
	ctx := context.Background()
	if _, err := plant.StartBatch(ctx, "line-1", 500, "north-intake"); err != nil {
		t.Fatalf("start batch: %v", err)
	}
	if _, err := plant.PublishRecipe(recipe.Draft{
		Source: "north-intake", Stage: model.StageReceiving, Chemical: "pac",
		DosePerCOD: 0.05, SampleMaxAge: time.Hour, MinRate: 1, MaxRate: 500,
	}); err != nil {
		t.Fatalf("publish recipe: %v", err)
	}
	sample := model.NewSample("line-1", "basin-line-1", model.SampleCOD, 1, 320, "mg/L", clock().Add(-time.Minute))
	if err := plant.ReceiveSample(ctx, sample); err != nil {
		t.Fatalf("receive sample: %v", err)
	}
	outcome, err := plant.CreateDosingSuggestion("line-1")
	if err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	if _, err := plant.EmergencyStop(ctx, "line-1", "safety drill"); err != nil {
		t.Fatalf("emergency stop: %v", err)
	}
	if _, _, err := plant.ConfirmDosingSuggestion(ctx, outcome.Suggestion.ID, "alice"); err == nil {
		t.Fatalf("expected confirmation to fail while line is isolated")
	}
}
