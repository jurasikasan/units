// Package health serves the liveness endpoint: it answers that the process
// is up and nothing else, per section 3 of CONTRACT.md.
package health

import (
	"net/http"

	httpHelper "github.com/jurasikasan/units/shared/http"
)

// Status is the body of GET /health.
type Status struct {
	Status string `json:"status"`
}

// Handler answers liveness requests.
type Handler struct{}

// NewHandler builds the health handler.
func NewHandler() *Handler {
	return &Handler{}
}

// Health reports that the process is up.
//
// @Summary      Liveness probe
// @Description  The process is up; nothing about its dependencies is checked.
// @Tags         health
// @Produce      json
// @Success      200  {object}  health.Status
// @Router       /health [get]
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	httpHelper.RespondWithJSON(w, http.StatusOK, &Status{Status: "ok"})
}
