package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/recipe"
)

type publishRecipeRequest struct {
	Source              string  `json:"source"`
	Stage               string  `json:"stage"`
	Chemical            string  `json:"chemical"`
	DosePerCOD          float64 `json:"dose_per_cod"`
	SampleMaxAgeSeconds float64 `json:"sample_max_age_seconds"`
	MinRate             float64 `json:"min_rate"`
	MaxRate             float64 `json:"max_rate"`
	Note                string  `json:"note"`
}

type confirmSuggestionRequest struct {
	Operator string `json:"operator"`
}

// listRecipes 返回当前有效配方版本；带 source/stage 参数时返回该键的全部版本。
func (s *Server) listRecipes(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	stage := r.URL.Query().Get("stage")
	if source != "" || stage != "" {
		stageValue, err := parseRecipeStage(stage)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		versions, err := s.plant.RecipeVersions(source, stageValue)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": s.plant.ActiveRecipes()})
}

func (s *Server) publishRecipe(w http.ResponseWriter, r *http.Request) {
	var input publishRecipeRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stage, err := parseRecipeStage(input.Stage)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.SampleMaxAgeSeconds <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("sample_max_age_seconds must be positive"))
		return
	}
	draft := recipe.Draft{
		Source: input.Source, Stage: stage, Chemical: input.Chemical,
		DosePerCOD: input.DosePerCOD, SampleMaxAge: time.Duration(input.SampleMaxAgeSeconds * float64(time.Second)),
		MinRate: input.MinRate, MaxRate: input.MaxRate, Note: input.Note,
	}
	published, err := s.plant.PublishRecipe(draft)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, published)
}

// previewSuggestion 只做计算：即使缺少可信样本也返回 200，由响应中的原因说明。
func (s *Server) previewSuggestion(w http.ResponseWriter, r *http.Request) {
	lineID, err := model.ParseLineID(chi.URLParam(r, "lineID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	outcome, err := s.plant.PreviewDosingSuggestion(lineID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, outcome)
}

// createSuggestion 计算并登记建议；条件不满足时返回 422 与原因，绝不产生设备命令。
func (s *Server) createSuggestion(w http.ResponseWriter, r *http.Request) {
	lineID, err := model.ParseLineID(chi.URLParam(r, "lineID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	outcome, err := s.plant.CreateDosingSuggestion(lineID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !outcome.Deliverable {
		writeJSON(w, http.StatusUnprocessableEntity, outcome)
		return
	}
	writeJSON(w, http.StatusCreated, outcome.Suggestion)
}

func (s *Server) listSuggestions(w http.ResponseWriter, r *http.Request) {
	var lineID model.LineID
	if raw := r.URL.Query().Get("line_id"); raw != "" {
		parsed, err := model.ParseLineID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		lineID = parsed
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": s.plant.DosingSuggestionHistory(lineID)})
}

// confirmSuggestion 仅在操作员显式确认后调用原有加药服务。
func (s *Server) confirmSuggestion(w http.ResponseWriter, r *http.Request) {
	suggestionID, err := uuid.Parse(chi.URLParam(r, "suggestionID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var input confirmSuggestionRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	suggestion, command, err := s.plant.ConfirmDosingSuggestion(r.Context(), suggestionID, input.Operator)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, recipe.ErrSuggestionNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "suggestion": suggestion})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"suggestion": suggestion, "command": command})
}

func parseRecipeStage(raw string) (model.ProcessStage, error) {
	stage := model.ProcessStage(raw)
	switch stage {
	case model.StageReceiving, model.StageEqualizing, model.StageBiological,
		model.StageSettling, model.StageQualified, model.StageDischarging:
		return stage, nil
	default:
		return "", errors.New("recipe stage is unsupported")
	}
}
