package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/recipe"
)

type recipeRequest struct {
	Code      string             `json:"code"`
	Source    string             `json:"source"`
	Stage     model.ProcessStage `json:"stage"`
	MinFlow   float64            `json:"min_flow"`
	MaxFlow   float64            `json:"max_flow"`
	Chemical  string             `json:"chemical"`
	DoseRatio float64            `json:"dose_ratio"`
}

func (s *Server) createRecipe(w http.ResponseWriter, r *http.Request) {
	var input recipeRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	registered, err := s.plant.RegisterRecipe(recipe.Draft{
		Code: input.Code, Source: input.Source, Stage: input.Stage,
		MinFlow: input.MinFlow, MaxFlow: input.MaxFlow,
		Chemical: input.Chemical, DoseRatio: input.DoseRatio,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, registered)
}

func (s *Server) listRecipes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"recipes": s.plant.Recipes()})
}

func (s *Server) recipeVersions(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	writeJSON(w, http.StatusOK, map[string]any{"recipes": s.plant.RecipeVersions(code)})
}

// recommendDose evaluates the line and answers with the outcome. A blocked
// outcome is a successful response carrying only the reason; no command is
// ever issued without an explicit confirm call.
func (s *Server) recommendDose(w http.ResponseWriter, r *http.Request) {
	lineID, err := model.ParseLineID(chi.URLParam(r, "lineID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	outcome, err := s.plant.RecommendDose(r.Context(), lineID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	status := http.StatusOK
	if outcome.OK {
		status = http.StatusCreated
	}
	writeJSON(w, status, outcome)
}

func (s *Server) listDosingRecommendations(w http.ResponseWriter, r *http.Request) {
	lineID, err := model.ParseLineID(chi.URLParam(r, "lineID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recommendations": s.plant.DosingRecommendations(lineID)})
}

func (s *Server) confirmDosingRecommendation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "recommendationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	command, err := s.plant.ConfirmDoseRecommendation(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, command)
}

func (s *Server) rejectDosingRecommendation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "recommendationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	recommendation, err := s.plant.RejectDoseRecommendation(id)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, recommendation)
}
