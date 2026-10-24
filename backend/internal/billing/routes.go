package billing

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

// Routes maps the billing API onto the permissions seeded in migration
// 000003. Reading money needs invoices.view or payments.view; moving money needs
// the matching create, reconcile or reverse permission, and nothing here lets a
// client write amount_paid, a ledger entry or a tenant credit directly.
func (h *Handler) Routes(r chi.Router) {
	r.With(rbac.Require("invoices.view")).Get("/invoices", h.listInvoices)
	r.With(rbac.Require("invoices.view")).Get("/invoices/{id}", h.getInvoice)
	r.With(rbac.Require("invoices.view")).Get("/invoices/{id}/ledger", h.invoiceLedger)
	r.With(rbac.Require("invoices.create")).Post("/invoices", h.createInvoice)
	r.With(rbac.Require("invoices.update")).Patch("/invoices/{id}", h.updateInvoice)
	r.With(rbac.Require("invoices.delete")).Delete("/invoices/{id}", h.deleteInvoice)
	r.With(rbac.Require("invoices.update")).Post("/invoices/{id}/issue", h.issueInvoice)
	r.With(rbac.Require("invoices.update")).Post("/invoices/{id}/void", h.voidInvoice)
	r.With(rbac.Require("invoices.update")).Post("/invoices/{id}/late-fee", h.assessLateFee)
	r.With(rbac.Require("payments.reconcile")).Post("/invoices/{id}/apply-credit", h.applyCredit)
	r.With(rbac.Require("invoices.update")).Post("/late-fees/{id}/waive", h.waiveLateFee)

	r.With(rbac.Require("payments.view")).Get("/payments", h.listPayments)
	r.With(rbac.Require("payments.view")).Get("/payments/{id}", h.getPayment)
	r.With(rbac.Require("payments.create")).Post("/payments", h.recordPayment)
	r.With(rbac.Require("payments.reverse")).Post("/payments/{id}/reverse", h.reversePayment)
	r.With(rbac.Require("payments.view")).Get("/leases/{id}/credit", h.leaseCredit)
	r.With(rbac.Require("payments.view")).Get("/payment-methods", h.paymentMethods)

	r.With(rbac.Require("invoices.view")).Get("/credit-notes", h.listCreditNotes)
	r.With(rbac.Require("invoices.view")).Get("/credit-notes/{id}", h.getCreditNote)
	r.With(rbac.Require("invoices.create")).Post("/credit-notes", h.createCreditNote)
	r.With(rbac.Require("invoices.update")).Patch("/credit-notes/{id}", h.updateCreditNote)
	r.With(rbac.Require("invoices.delete")).Delete("/credit-notes/{id}", h.deleteCreditNote)
	r.With(rbac.Require("invoices.update")).Post("/credit-notes/{id}/issue", h.issueCreditNote)
	r.With(rbac.Require("invoices.update")).Post("/credit-notes/{id}/apply", h.applyCreditNote)
	r.With(rbac.Require("invoices.update")).Post("/credit-notes/{id}/void", h.voidCreditNote)

	r.With(rbac.Require("invoices.view")).Get("/ledger/entries", h.listLedger)
	r.With(rbac.Require("invoices.view")).Get("/ledger/accounts", h.listAccounts)
	r.With(rbac.Require("invoices.view")).Get("/billing/summary", h.summary)
}
