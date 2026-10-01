package process

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/recipe"
)

func newRecipePlant(t *testing.T) *Plant {
	t.Helper()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	plant, err := NewPlant(t.TempDir(), []model.LineID{"line-1"}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new plant: %v", err)
	}
	return plant
}

func startRecipeBatch(t *testing.T, plant *Plant) model.Batch {
	t.Helper()
	batch, err := plant.StartBatch(context.Background(), "line-1", 500, "north-intake")
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	if _, err := plant.RegisterRecipe(recipe.Draft{
		Code: "carbon-north", Source: "north-intake", Stage: model.StageReceiving,
		MinFlow: 100, MaxFlow: 1000, Chemical: "carbon", DoseRatio: 0.8,
	}); err != nil {
		t.Fatalf("register recipe: %v", err)
	}
	return batch
}

func receiveCOD(t *testing.T, plant *Plant, sequence uint64, value float64) {
	t.Helper()
	sample := model.NewSample("line-1", "basin-line-1", model.SampleCOD, sequence, value, "mg/L", plant.Clock())
	if err := plant.ReceiveSample(context.Background(), sample); err != nil {
		t.Fatalf("receive cod sample: %v", err)
	}
}

func TestRecommendDoseBlocksWithoutTrustworthySample(t *testing.T) {
	plant := newRecipePlant(t)
	startRecipeBatch(t, plant)
	outcome, err := plant.RecommendDose(context.Background(), "line-1")
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if outcome.OK || outcome.Reason == "" {
		t.Fatalf("missing cod sample must block with a reason, got %+v", outcome)
	}
	if len(plant.DosingRecommendations("line-1")) != 0 {
		t.Fatal("blocked evaluation must not record a recommendation")
	}
	if len(plant.devices.All()) != 0 {
		t.Fatal("blocked evaluation must not issue any device command")
	}
}

func TestConfirmDoseRecommendationCallsOriginalDosingService(t *testing.T) {
	plant := newRecipePlant(t)
	batch := startRecipeBatch(t, plant)
	receiveCOD(t, plant, 1, 120)
	outcome, err := plant.RecommendDose(context.Background(), "line-1")
	if err != nil || !outcome.OK {
		t.Fatalf("recommend: %v %+v", err, outcome)
	}
	if outcome.Recommendation.Rate != 48 {
		t.Fatalf("expected rate 48, got %v", outcome.Recommendation.Rate)
	}
	if len(plant.devices.All()) != 0 {
		t.Fatal("evaluation alone must not command the doser")
	}
	command, err := plant.ConfirmDoseRecommendation(context.Background(), outcome.Recommendation.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if command.BatchID != batch.ID || command.Kind != model.DeviceDoser || command.Setpoint != 48 {
		t.Fatalf("unexpected dosing command %+v", command)
	}
	recorded := plant.DosingRecommendations("line-1")
	if len(recorded) != 1 || recorded[0].Status != recipe.StatusConfirmed {
		t.Fatalf("recommendation must be confirmed, got %+v", recorded)
	}
	if recorded[0].CommandID == nil || *recorded[0].CommandID != command.ID {
		t.Fatal("confirmed recommendation must reference the issued command")
	}
	if _, err := plant.ConfirmDoseRecommendation(context.Background(), outcome.Recommendation.ID); err == nil {
		t.Fatal("confirming twice must fail")
	}
	if _, err := plant.ConfirmDoseRecommendation(context.Background(), uuid.New()); err == nil {
		t.Fatal("confirming an unknown recommendation must fail")
	}
}

func TestRejectDoseRecommendationSkipsCommand(t *testing.T) {
	plant := newRecipePlant(t)
	startRecipeBatch(t, plant)
	receiveCOD(t, plant, 1, 120)
	outcome, err := plant.RecommendDose(context.Background(), "line-1")
	if err != nil || !outcome.OK {
		t.Fatalf("recommend: %v %+v", err, outcome)
	}
	rejected, err := plant.RejectDoseRecommendation(outcome.Recommendation.ID)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != recipe.StatusRejected {
		t.Fatalf("expected rejected, got %s", rejected.Status)
	}
	if _, err := plant.ConfirmDoseRecommendation(context.Background(), outcome.Recommendation.ID); err == nil {
		t.Fatal("rejected recommendation must not be confirmable")
	}
	if len(plant.devices.All()) != 0 {
		t.Fatal("rejected recommendation must not issue any device command")
	}
}

func TestRecipeUpdateDoesNotRewriteHistory(t *testing.T) {
	plant := newRecipePlant(t)
	startRecipeBatch(t, plant)
	receiveCOD(t, plant, 1, 120)
	first, err := plant.RecommendDose(context.Background(), "line-1")
	if err != nil || !first.OK {
		t.Fatalf("first recommend: %v %+v", err, first)
	}
	if _, err := plant.RegisterRecipe(recipe.Draft{
		Code: "carbon-north", Source: "north-intake", Stage: model.StageReceiving,
		MinFlow: 100, MaxFlow: 1000, Chemical: "carbon", DoseRatio: 1.2,
	}); err != nil {
		t.Fatalf("register recipe v2: %v", err)
	}
	second, err := plant.RecommendDose(context.Background(), "line-1")
	if err != nil || !second.OK {
		t.Fatalf("second recommend: %v %+v", err, second)
	}
	if second.Recommendation.RecipeVersion != 2 || second.Recommendation.Rate != 72 {
		t.Fatalf("new evaluation must use v2, got %+v", second.Recommendation)
	}
	history := plant.DosingRecommendations("line-1")
	if len(history) != 2 {
		t.Fatalf("expected two recommendations, got %+v", history)
	}
	original := history[1]
	if original.ID != first.Recommendation.ID {
		t.Fatalf("history order changed unexpectedly: %+v", history)
	}
	if original.RecipeVersion != 1 || original.Rate != 48 || original.Basis.DoseRatio != 0.8 {
		t.Fatalf("recipe update rewrote a historical recommendation: %+v", original)
	}
}
