package process

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/recipe"
)

// dosingRecipeContext 组装建议计算所需的现场快照：线状态、活跃批次、
// 批次接纳时的进水来源与流量、当前线最近一条 COD 在线样本。
func (p *Plant) dosingRecipeContext(lineID model.LineID) (recipe.Context, error) {
	p.mu.RLock()
	line, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok {
		return recipe.Context{}, errors.New("process line is not found")
	}
	batch, ok := p.ActiveBatch(lineID)
	if !ok {
		return recipe.Context{}, errors.New("process line has no active batch")
	}
	context := recipe.Context{Line: line, Batch: batch}
	if admission, found := p.intake.Current(lineID); found {
		context.Source = admission.Source
		context.FlowM3Hour = admission.FlowM3Hour
	}
	if sample, found := p.sampler.Latest(lineID, model.SampleCOD); found {
		context.Sample = &sample
	}
	return context, nil
}

// PreviewDosingSuggestion 只计算建议、不登记也不下发。
// 缺失可信样本时返回的 Outcome 中 Delieverable 为 false，Reasons 说明原因。
func (p *Plant) PreviewDosingSuggestion(lineID model.LineID) (recipe.Outcome, error) {
	context, err := p.dosingRecipeContext(lineID)
	if err != nil {
		return recipe.Outcome{}, err
	}
	return p.recipeEngine.Evaluate(context, p.now()), nil
}

// CreateDosingSuggestion 计算并登记一条建议。条件不满足时不登记、不下发，
// 返回的 Outcome 仅携带阻断原因供界面展示。
func (p *Plant) CreateDosingSuggestion(lineID model.LineID) (recipe.Outcome, error) {
	context, err := p.dosingRecipeContext(lineID)
	if err != nil {
		return recipe.Outcome{}, err
	}
	outcome := p.recipeEngine.Evaluate(context, p.now())
	if !outcome.Deliverable {
		return outcome, nil
	}
	p.suggestions.Save(outcome.Suggestion)
	p.suggestions.MarkSuperseded(outcome.Suggestion.BatchID, outcome.Suggestion.ID, p.now())
	return outcome, nil
}

// ConfirmDosingSuggestion 由操作员确认建议后，调用原有加药服务下发命令。
// 建议只能确认一次；加药服务调用失败时建议回到 failed，可重新确认。
func (p *Plant) ConfirmDosingSuggestion(ctx context.Context, suggestionID uuid.UUID, operator string) (recipe.Suggestion, model.DeviceCommand, error) {
	if err := ctx.Err(); err != nil {
		return recipe.Suggestion{}, model.DeviceCommand{}, err
	}
	suggestion, ok := p.suggestions.Current(suggestionID)
	if !ok {
		return recipe.Suggestion{}, model.DeviceCommand{}, recipe.ErrSuggestionNotFound
	}
	p.mu.RLock()
	line, lineOK := p.lines[suggestion.LineID]
	p.mu.RUnlock()
	if !lineOK {
		return recipe.Suggestion{}, model.DeviceCommand{}, errors.New("process line is not found")
	}
	if line.Emergency {
		return recipe.Suggestion{}, model.DeviceCommand{}, errors.New("process line is isolated and cannot change dosing")
	}
	active, hasBatch := p.ActiveBatch(suggestion.LineID)
	if !hasBatch || active.ID != suggestion.BatchID {
		return recipe.Suggestion{}, model.DeviceCommand{}, recipe.ErrSuggestionBatchMismatch
	}
	operator = strings.TrimSpace(operator)
	if operator == "" {
		operator = "operator"
	}
	confirmed, err := p.suggestions.Confirm(suggestionID, operator, p.now())
	if err != nil {
		return recipe.Suggestion{}, model.DeviceCommand{}, err
	}
	command, err := p.ChangeDose(ctx, suggestion.LineID, suggestion.BatchID, suggestion.Chemical, suggestion.Rate)
	if err != nil {
		failed, failErr := p.suggestions.Fail(suggestionID, err.Error(), p.now())
		if failErr != nil {
			return recipe.Suggestion{}, model.DeviceCommand{}, failErr
		}
		return failed, model.DeviceCommand{}, err
	}
	completed, err := p.suggestions.Complete(suggestionID, command.ID, p.now())
	if err != nil {
		return confirmed, command, err
	}
	return completed, command, nil
}

// PublishRecipe 发布配方新版本，旧版本自动作废，但不影响已生成的历史建议。
func (p *Plant) PublishRecipe(draft recipe.Draft) (recipe.Recipe, error) {
	return p.recipes.Publish(draft, p.now())
}

func (p *Plant) ActiveRecipes() []recipe.Recipe {
	return p.recipes.ActiveRecipes()
}

func (p *Plant) RecipeVersions(source string, stage model.ProcessStage) ([]recipe.Recipe, error) {
	key, err := recipe.NewKey(source, stage)
	if err != nil {
		return nil, err
	}
	return p.recipes.Versions(key), nil
}

func (p *Plant) Suggestion(id uuid.UUID) (recipe.Suggestion, bool) {
	return p.suggestions.Current(id)
}

// DosingSuggestionHistory 返回建议历史。lineID 为空时返回全部处理线。
func (p *Plant) DosingSuggestionHistory(lineID model.LineID) []recipe.Suggestion {
	if lineID == "" {
		return p.suggestions.History(uuid.Nil)
	}
	p.mu.RLock()
	batchIDs := make(map[uuid.UUID]struct{})
	for id, batch := range p.batches {
		if batch.LineID == lineID {
			batchIDs[id] = struct{}{}
		}
	}
	p.mu.RUnlock()
	result := make([]recipe.Suggestion, 0)
	for batchID := range batchIDs {
		result = append(result, p.suggestions.History(batchID)...)
	}
	return result
}
