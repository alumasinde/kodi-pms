package utilities

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

// Routes mounts the utility surface. Reading writes sit on their own permission
// because recording what a dial says is a caretaker's job, while changing a
// meter or a charge is a property manager's.
func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("utilities.view")).Get("/utility/meters", h.listMeters)
	r.With(rbac.Require("utilities.view")).Get("/utility/meters/lookups", h.lookups)
	r.With(rbac.Require("utilities.view")).Get("/utility/meters/{id}", h.getMeter)
	r.With(rbac.Require("utilities.create")).Post("/utility/meters", h.createMeter)
	r.With(rbac.Require("utilities.update")).Patch("/utility/meters/{id}", h.updateMeter)
	r.With(rbac.Require("utilities.update")).Delete("/utility/meters/{id}", h.deleteMeter)

	r.With(rbac.Require("utilities.view")).Get("/utility/meters/{id}/readings", h.listReadings)
	r.With(rbac.Require("utilities.record_reading")).Post("/utility/meters/{id}/readings", h.createReading)
	r.With(rbac.Require("utilities.view")).Get("/utility/readings/{id}", h.getReading)
	r.With(rbac.Require("utilities.record_reading")).Patch("/utility/readings/{id}", h.updateReading)
	r.With(rbac.Require("utilities.record_reading")).Delete("/utility/readings/{id}", h.deleteReading)

	r.With(rbac.Require("utilities.view")).Get("/utility/bills", h.listBills)
	r.With(rbac.Require("utilities.create")).Post("/utility/bills", h.createBill)
	r.With(rbac.Require("utilities.create")).Post("/utility/bills/generate", h.generateBills)
	r.With(rbac.Require("utilities.view")).Get("/utility/bills/{id}", h.getBill)
	r.With(rbac.Require("utilities.update")).Patch("/utility/bills/{id}", h.updateBill)
	r.With(rbac.Require("utilities.update")).Post("/utility/bills/{id}/void", h.voidBill)

	r.With(rbac.Require("utilities.view")).Get("/utility/summary", h.summary)
}
