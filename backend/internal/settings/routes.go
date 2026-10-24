package settings

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("settings.view")).Get("/settings", h.get)
	r.With(rbac.Require("settings.update")).Patch("/settings", h.update)
}
