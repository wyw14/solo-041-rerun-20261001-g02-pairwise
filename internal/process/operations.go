package process

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/aeration"
	"github.com/wyw14/cry-97/internal/dosing"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/recipe"
	"github.com/wyw14/cry-97/internal/settling"
)

func (p *Plant) SubmitAeration(ctx context.Context, lineID model.LineID, basinID string, payload []byte) (aeration.Window, *model.DeviceCommand, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return aeration.Window{}, nil, errors.New("process line cannot accept aeration samples")
	}
	return p.aeration.SubmitWindow(ctx, lineID, basinID, payload, state.Generation, p.now())
}

func (p *Plant) ChangeDose(ctx context.Context, lineID model.LineID, batchID uuid.UUID, chemical string, rate float64) (model.DeviceCommand, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return model.DeviceCommand{}, errors.New("process line cannot change dosing")
	}
	change, err := dosing.NewChange(lineID, batchID, chemical, rate, state.Generation, p.now())
	if err != nil {
		return model.DeviceCommand{}, err
	}
	return p.dosing.ChangeRate(ctx, change, p.now())
}

func (p *Plant) RegisterRecipe(draft recipe.Draft) (recipe.Recipe, error) {
	return p.recipes.Register(draft, p.now())
}

func (p *Plant) Recipes() []recipe.Recipe {
	return p.recipes.Latest()
}

func (p *Plant) RecipeVersions(code string) []recipe.Recipe {
	return p.recipes.Versions(code)
}

// RecommendDose evaluates the current line context and, when every input is
// trustworthy, records a pending recommendation. A blocked outcome only
// carries the reason; it never issues a command.
func (p *Plant) RecommendDose(ctx context.Context, lineID model.LineID) (recipe.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return recipe.Outcome{}, err
	}
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok {
		return recipe.Outcome{}, errors.New("process line is not found")
	}
	input := recipe.Input{Line: state, Recipes: p.recipes, Now: p.now(), MaxSampleAge: recipe.DefaultMaxSampleAge}
	if batch, hasBatch := p.ActiveBatch(lineID); hasBatch {
		input.Batch = &batch
	}
	if admission, found := p.intake.Current(lineID); found {
		input.Admission = &admission
	}
	for _, sample := range p.sampler.LineSnapshot(lineID) {
		if sample.Kind == model.SampleCOD {
			latest := sample
			input.Sample = &latest
		}
	}
	outcome := recipe.Evaluate(input)
	if !outcome.OK {
		return outcome, nil
	}
	if err := p.recommendBook.Add(*outcome.Recommendation); err != nil {
		return recipe.Outcome{}, err
	}
	return outcome, nil
}

func (p *Plant) DosingRecommendations(lineID model.LineID) []recipe.Recommendation {
	return p.recommendBook.ForLine(lineID)
}

// ConfirmDoseRecommendation executes a pending recommendation through the
// original dosing service. The recommendation keeps its recorded rate; only
// its status changes.
func (p *Plant) ConfirmDoseRecommendation(ctx context.Context, id uuid.UUID) (model.DeviceCommand, error) {
	p.decideMu.Lock()
	defer p.decideMu.Unlock()
	recommendation, ok := p.recommendBook.Get(id)
	if !ok {
		return model.DeviceCommand{}, errors.New("dosing recommendation is not found")
	}
	if recommendation.Status != recipe.StatusPending {
		return model.DeviceCommand{}, errors.New("dosing recommendation is already decided")
	}
	batch, hasBatch := p.ActiveBatch(recommendation.LineID)
	if !hasBatch || batch.ID != recommendation.BatchID {
		return model.DeviceCommand{}, errors.New("recommendation batch is no longer active")
	}
	command, err := p.ChangeDose(ctx, recommendation.LineID, recommendation.BatchID, recommendation.Chemical, recommendation.Rate)
	if err != nil {
		return model.DeviceCommand{}, err
	}
	if _, err := p.recommendBook.Decide(id, recipe.StatusConfirmed, &command.ID, p.now()); err != nil {
		return model.DeviceCommand{}, err
	}
	return command, nil
}

func (p *Plant) RejectDoseRecommendation(id uuid.UUID) (recipe.Recommendation, error) {
	p.decideMu.Lock()
	defer p.decideMu.Unlock()
	return p.recommendBook.Decide(id, recipe.StatusRejected, nil, p.now())
}

func (p *Plant) UpdateBlanket(lineID model.LineID, level float64) (settling.Snapshot, error) {
	state, ok := p.settling[lineID]
	if !ok {
		return settling.Snapshot{}, errors.New("settling line is not found")
	}
	return state.UpdateBlanket(level, 2.8, p.now()), nil
}

func (p *Plant) ReceiveSample(ctx context.Context, sample model.Sample) error {
	if err := p.receiver.Receive(ctx, sample, p.now()); err != nil {
		return err
	}
	return p.sampler.Publish(sample)
}

func (p *Plant) BeginSettling(lineID model.LineID) (settling.Cycle, error) {
	state, ok := p.settling[lineID]
	if !ok {
		return settling.Cycle{}, errors.New("settling line is not found")
	}
	state.BeginCycle(p.now())
	return p.cycles.Start(lineID, p.now())
}

func (p *Plant) AdvanceSettling(lineID model.LineID) (settling.Cycle, error) {
	return p.cycles.Advance(lineID, p.now())
}

func (p *Plant) Alarms() []model.Alarm {
	return p.alarms.All()
}

func (p *Plant) StatusBlowers() []aeration.Blower {
	return p.aeration.Blowers()
}

func (p *Plant) StatusAerationWindows() []aeration.Window {
	return p.aeration.Windows()
}

func (p *Plant) AcknowledgeAlarm(id uuid.UUID) (model.Alarm, error) {
	return p.alarms.Acknowledge(id)
}

func (p *Plant) RecoverAlarm(id uuid.UUID) (model.Alarm, error) {
	return p.alarms.Recover(id, p.now())
}

func (p *Plant) Clock() time.Time {
	return p.now().UTC()
}
