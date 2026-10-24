package mpesa

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

// Routes mounts the operator surface under the authenticated API group.
//
// Four permissions split it. mpesa.view reads everything; mpesa.manage changes a
// paybill's configuration, which is the one action that can silently stop receipts
// arriving; mpesa.push asks a tenant for money; and mpesa.reconcile moves an
// unmatched receipt into a ledger, which is the only write in this module that
// creates a payment without an invoice behind it.
func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("mpesa.view")).Get("/mpesa/shortcodes", h.listShortcodes)
	r.With(rbac.Require("mpesa.view")).Get("/mpesa/shortcodes/lookups", h.lookups)
	r.With(rbac.Require("mpesa.view")).Get("/mpesa/shortcodes/{id}", h.getShortcode)
	r.With(rbac.Require("mpesa.manage")).Post("/mpesa/shortcodes", h.createShortcode)
	r.With(rbac.Require("mpesa.manage")).Patch("/mpesa/shortcodes/{id}", h.updateShortcode)
	r.With(rbac.Require("mpesa.manage")).Post("/mpesa/shortcodes/{id}/rotate-token", h.rotateToken)
	r.With(rbac.Require("mpesa.manage")).Delete("/mpesa/shortcodes/{id}", h.deleteShortcode)

	r.With(rbac.Require("mpesa.view")).Get("/mpesa/account-refs", h.listRefs)
	r.With(rbac.Require("mpesa.manage")).Post("/mpesa/account-refs", h.createRef)
	r.With(rbac.Require("mpesa.manage")).Delete("/mpesa/account-refs/{id}", h.deleteRef)

	r.With(rbac.Require("mpesa.view")).Get("/mpesa/receipts", h.listReceipts)
	r.With(rbac.Require("mpesa.view")).Get("/mpesa/receipts/{id}", h.getReceipt)
	r.With(rbac.Require("mpesa.reconcile")).Post("/mpesa/receipts/{id}/match", h.matchReceipt)
	r.With(rbac.Require("mpesa.reconcile")).Post("/mpesa/receipts/{id}/ignore", h.ignoreReceipt)

	r.With(rbac.Require("mpesa.view")).Get("/mpesa/stk", h.listPrompts)
	r.With(rbac.Require("mpesa.push")).Post("/mpesa/stk/push", h.push)
	r.With(rbac.Require("mpesa.view")).Get("/mpesa/stk/{id}", h.getPrompt)
}

// WebhookRoutes mounts the three paths Safaricom is pointed at. They are on the
// root router, not under /api/v1, for two reasons that are the same reason: a
// callback arrives with no host that resolves an organization, so it cannot pass
// the tenancy resolver, and with no bearer token, so it cannot pass the auth
// middleware either. What it carries is the token in its URL, and that token is
// resolved by HMAC alone.
//
// Nothing here returns a non-2xx status. The verdict a webhook gives is in the
// ResultCode it answers with, and a status Safaricom reads as a delivery failure
// makes it retry a receipt this installation already saw.
func (h *Handler) WebhookRoutes(r chi.Router) {
	r.Post("/integrations/mpesa/c2b/{token}/validate", h.validateReceipt)
	r.Post("/integrations/mpesa/c2b/{token}/confirm", h.confirmReceipt)
	r.Post("/integrations/mpesa/stk/{token}/callback", h.stkCallback)
}
