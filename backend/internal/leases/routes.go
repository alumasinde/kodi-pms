package leases

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("leases.view")).Get("/leases", h.list)
	r.With(rbac.Require("leases.view")).Get("/leases/{id}", h.get)
	r.With(rbac.Require("leases.view")).Get("/leases/{id}/tenants", h.tenants)
	r.With(rbac.Require("leases.create")).Post("/leases", h.create)
	r.With(rbac.Require("leases.update")).Patch("/leases/{id}", h.update)
	r.With(rbac.Require("leases.update")).Put("/leases/{id}/tenants", h.replaceTenants)
	r.With(rbac.Require("leases.delete")).Delete("/leases/{id}", h.delete)
	r.With(rbac.Require("leases.activate")).Post("/leases/{id}/activate", h.activate)
	r.With(rbac.Require("leases.terminate")).Post("/leases/{id}/terminate", h.terminate)
	r.With(rbac.Require("leases.renew")).Post("/leases/{id}/renew", h.renew)
}
