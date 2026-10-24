package billing

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

type Handler struct {
	svc *Service
	cfg *config.Config
}

func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{svc: svc, cfg: cfg}
}

func (h *Handler) listInvoices(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	q, err := invoiceQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Invoices(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Invoices, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) getInvoice(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Invoice(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) invoiceLedger(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.InvoiceLedger(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) createInvoice(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	var in CreateInvoiceInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreateInvoice(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) updateInvoice(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in UpdateInvoiceInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.UpdateInvoice(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) deleteInvoice(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteInvoice(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

// issue posts an invoice: it is the only invoice action that moves money into
// the ledger, so it takes its own permission check inside the service.
func (h *Handler) issueInvoice(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in IssueInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.IssueInvoice(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) voidInvoice(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in VoidInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.VoidInvoice(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) assessLateFee(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in LateFeeInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.AssessLateFee(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) waiveLateFee(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in WaiveInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.WaiveLateFee(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) applyCredit(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in ApplyCreditInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.ApplyCredit(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) listPayments(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	q, err := paymentQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Payments(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Payments, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) getPayment(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Payment(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) recordPayment(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	var in PaymentInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.RecordPayment(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) reversePayment(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in ReversePaymentInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.ReversePayment(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) leaseCredit(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.LeaseCredit(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) listCreditNotes(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	q, err := creditNoteQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreditNotes(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.CreditNotes, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) getCreditNote(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.CreditNote(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) createCreditNote(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	var in CreateCreditNoteInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreateCreditNote(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) updateCreditNote(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in UpdateCreditNoteInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.UpdateCreditNote(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) issueCreditNote(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in IssueCreditNoteInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.IssueCreditNote(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) applyCreditNote(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	out, err := h.svc.ApplyCreditNote(r.Context(), p, id, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) voidCreditNote(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	var in VoidCreditNoteInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.VoidCreditNote(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) deleteCreditNote(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.principalID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteCreditNote(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) paymentMethods(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	out, err := h.svc.PaymentMethods(r.Context(), p)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) listLedger(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	q, err := ledgerQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Ledger(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Entries, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	out, err := h.svc.LedgerAccounts(r.Context(), p)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	p := h.principal(w, r)
	if p == nil {
		return
	}
	q, err := invoiceQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Summary(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) principal(w http.ResponseWriter, r *http.Request) *rbac.Principal {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return nil
	}
	return p
}

func (h *Handler) principalID(w http.ResponseWriter, r *http.Request) (*rbac.Principal, uint64, bool) {
	p := h.principal(w, r)
	if p == nil {
		return nil, 0, false
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return nil, 0, false
	}
	return p, id, true
}

func (h *Handler) meta(r *http.Request) ClientMeta {
	return ClientMeta{
		IP:        httpx.ClientIP(r, h.cfg.TrustProxy),
		UserAgent: r.UserAgent(),
	}
}

func invoiceQuery(r *http.Request) (InvoiceQuery, error) {
	q := InvoiceQuery{
		Search: r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Type:   r.URL.Query().Get("invoice_type"),
		Page:   httpx.Pagination(r),
		Sort:   httpx.SortParam(r, invoiceSortColumns, defaultInvoiceSort),
	}
	for _, item := range []struct {
		name string
		dest *uint64
	}{
		{"lease_id", &q.LeaseID}, {"property_id", &q.PropertyID},
		{"unit_id", &q.UnitID}, {"tenant_id", &q.TenantID},
	} {
		id, err := queryID(r, item.name)
		if err != nil {
			return InvoiceQuery{}, err
		}
		*item.dest = id
	}
	overdue, err := queryBool(r, "overdue")
	if err != nil {
		return InvoiceQuery{}, err
	}
	q.Overdue = overdue
	if q.DueFrom, q.DueTo, err = queryDatePair(r, "due_from", "due_to"); err != nil {
		return InvoiceQuery{}, err
	}
	return q, nil
}

func paymentQuery(r *http.Request) (PaymentQuery, error) {
	q := PaymentQuery{
		Search: r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Page:   httpx.Pagination(r),
		Sort:   httpx.SortParam(r, paymentSortColumns, defaultPaymentSort),
	}
	for _, item := range []struct {
		name string
		dest *uint64
	}{
		{"lease_id", &q.LeaseID}, {"property_id", &q.PropertyID}, {"method_id", &q.MethodID},
	} {
		id, err := queryID(r, item.name)
		if err != nil {
			return PaymentQuery{}, err
		}
		*item.dest = id
	}
	var err error
	if q.From, q.To, err = queryDatePair(r, "from", "to"); err != nil {
		return PaymentQuery{}, err
	}
	return q, nil
}

func ledgerQuery(r *http.Request) (LedgerQuery, error) {
	q := LedgerQuery{
		AccountCode: r.URL.Query().Get("account"),
		SourceType:  r.URL.Query().Get("source_type"),
		Page:        httpx.Pagination(r),
		Sort:        httpx.SortParam(r, ledgerSortColumns, defaultLedgerSort),
	}
	for _, item := range []struct {
		name string
		dest *uint64
	}{
		{"source_id", &q.SourceID}, {"lease_id", &q.LeaseID}, {"property_id", &q.PropertyID},
	} {
		id, err := queryID(r, item.name)
		if err != nil {
			return LedgerQuery{}, err
		}
		*item.dest = id
	}
	var err error
	if q.From, q.To, err = queryDatePair(r, "from", "to"); err != nil {
		return LedgerQuery{}, err
	}
	return q, nil
}

func creditNoteQuery(r *http.Request) (CreditNoteQuery, error) {
	q := CreditNoteQuery{
		Search: r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Page:   httpx.Pagination(r),
		Sort:   httpx.SortParam(r, creditNoteSortColumns, defaultCreditNoteSort),
	}
	for _, item := range []struct {
		name string
		dest *uint64
	}{
		{"lease_id", &q.LeaseID}, {"invoice_id", &q.InvoiceID}, {"property_id", &q.PropertyID},
	} {
		id, err := queryID(r, item.name)
		if err != nil {
			return CreditNoteQuery{}, err
		}
		*item.dest = id
	}
	return q, nil
}

func queryID(r *http.Request, name string) (uint64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, httpx.Validation(map[string]string{name: "must be a positive integer"})
	}
	return id, nil
}

func queryBool(r *http.Request, name string) (bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, httpx.Validation(map[string]string{name: "must be true or false"})
	}
	return value, nil
}

// queryDatePair reads an inclusive date range. Only the ends are checked here;
// the service rejects a range whose start is after its end.
func queryDatePair(r *http.Request, fromName, toName string) (time.Time, time.Time, error) {
	from, err := queryDate(r, fromName)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := queryDate(r, toName)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from, to, nil
}

func queryDate(r *http.Request, name string) (time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(dateLayout, raw)
	if err != nil {
		return time.Time{}, httpx.Validation(map[string]string{
			name: "must be a date formatted as " + dateLayout})
	}
	return value, nil
}

func pathID(r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// decodeOptional reads an action body when the client sends one. Assessing a
// late fee or voiding an invoice has no required fields, so an empty body is
// still a valid request.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) error {
	err := httpx.Decode(w, r, dst)
	var e *httpx.Err
	if errors.As(err, &e) && e.Fields["body"] == "required" {
		return nil
	}
	return err
}
