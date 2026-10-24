package units

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("units.view")).Get("/units", h.list)
	r.With(rbac.Require("units.view")).Get("/units/lookups", h.lookups)
	r.With(rbac.Require("units.view")).Get("/units/{id}", h.get)
	r.With(rbac.Require("units.create")).Post("/units", h.create)
	r.With(rbac.Require("units.create")).Post("/units/generate", h.generate)
	r.With(rbac.Require("units.update")).Patch("/units/{id}", h.update)
	r.With(rbac.Require("units.delete")).Delete("/units/{id}", h.delete)
}
