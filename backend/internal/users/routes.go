package users

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("users.view")).Get("/users", h.list)
	r.With(rbac.Require("users.view")).Get("/users/lookups", h.lookups)
	r.With(rbac.Require("users.view")).Get("/users/{id}", h.get)
	r.With(rbac.Require("users.view")).Get("/users/{id}/assignments", h.listAssignments)
	r.With(rbac.Require("users.create")).Post("/users", h.create)
	r.With(rbac.Require("users.update")).Patch("/users/{id}", h.update)
	r.With(rbac.Require("users.update")).Put("/users/{id}/assignments", h.replaceAssignments)
	r.With(rbac.Require("users.delete")).Delete("/users/{id}", h.delete)
}
