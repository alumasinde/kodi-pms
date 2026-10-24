package properties

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("properties.view")).Get("/properties", h.list)
	r.With(rbac.Require("properties.view")).Get("/properties/lookups", h.lookups)
	r.With(rbac.Require("properties.view")).Get("/properties/{id}", h.get)
	r.With(rbac.Require("properties.create")).Post("/properties", h.create)
	r.With(rbac.Require("properties.update")).Patch("/properties/{id}", h.update)
	r.With(rbac.Require("properties.delete")).Delete("/properties/{id}", h.delete)
}
