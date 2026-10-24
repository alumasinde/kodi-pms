package utilities

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

func (h *Handler) listMeters(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := meterQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Meters(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Meters, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) lookups(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Lookups(r.Context(), p)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) getMeter(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.Meter(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) createMeter(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in CreateMeterInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreateMeter(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) updateMeter(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in UpdateMeterInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.UpdateMeter(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) deleteMeter(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	if err := h.svc.DeleteMeter(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) listReadings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	q, err := readingQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Readings(r.Context(), p, id, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Readings, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) createReading(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in CreateReadingInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreateReading(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) getReading(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.Reading(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) updateReading(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in UpdateReadingInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.UpdateReading(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) deleteReading(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	if err := h.svc.DeleteReading(r.Context(), p, id, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) listBills(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := billQuery(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Bills(r.Context(), p, q)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out.Bills, httpx.PageMeta(q.Page, out.Total))
}

func (h *Handler) getBill(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	out, err := h.svc.Bill(r.Context(), p, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) createBill(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in CreateBillInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.CreateBill(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) generateBills(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in GenerateBillsInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.GenerateBills(r.Context(), p, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Created(w, out)
}

func (h *Handler) updateBill(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in UpdateBillInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.UpdateBill(r.Context(), p, id, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) voidBill(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		httpx.Fail(w, httpx.ErrNotFound)
		return
	}
	var in VoidBillInput
	if err := decodeOptional(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.VoidBill(r.Context(), p, id, in.Reason, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	propertyID, err := queryID(r, "property_id")
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.svc.Summary(r.Context(), p, propertyID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, out, nil)
}

func (h *Handler) meta(r *http.Request) ClientMeta {
	return ClientMeta{
		IP:        httpx.ClientIP(r, h.cfg.TrustProxy),
		UserAgent: r.UserAgent(),
	}
}

// decodeOptional reads an action body a caller may leave out. Voiding a charge
// takes only a reason, and a caretaker with nothing to write is not sending a
// malformed request.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) error {
	err := httpx.Decode(w, r, dst)
	var e *httpx.Err
	if errors.As(err, &e) && e.Fields["body"] == "required" {
		return nil
	}
	return err
}

func principal(w http.ResponseWriter, r *http.Request) (*rbac.Principal, bool) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return nil, false
	}
	return p, true
}

func meterQuery(r *http.Request) (MeterQuery, error) {
	q := MeterQuery{
		Search:      r.URL.Query().Get("q"),
		UtilityType: r.URL.Query().Get("utility_type"),
		Status:      r.URL.Query().Get("status"),
		OpenOnly:    queryFlag(r, "open_only"),
		Page:        httpx.Pagination(r),
		Sort:        httpx.SortParam(r, meterSortColumns, defaultMeterSort),
	}
	propertyID, err := queryID(r, "property_id")
	if err != nil {
		return q, err
	}
	unitID, err := queryID(r, "unit_id")
	if err != nil {
		return q, err
	}
	q.PropertyID = propertyID
	q.UnitID = unitID
	return q, nil
}

func readingQuery(r *http.Request) (ReadingQuery, error) {
	q := ReadingQuery{
		Page: httpx.Pagination(r),
		Sort: httpx.SortParam(r, readingSortColumns, defaultReadingSort),
	}
	from, err := queryDate(r, "from")
	if err != nil {
		return q, err
	}
	to, err := queryDate(r, "to")
	if err != nil {
		return q, err
	}
	q.From = from
	q.To = to
	return q, nil
}

func billQuery(r *http.Request) (BillQuery, error) {
	q := BillQuery{
		Search:  r.URL.Query().Get("q"),
		Status:  r.URL.Query().Get("status"),
		Type:    r.URL.Query().Get("type"),
		Overdue: queryFlag(r, "overdue"),
		Page:    httpx.Pagination(r),
		Sort:    httpx.SortParam(r, billSortColumns, defaultBillSort),
	}
	propertyID, err := queryID(r, "property_id")
	if err != nil {
		return q, err
	}
	unitID, err := queryID(r, "unit_id")
	if err != nil {
		return q, err
	}
	meterID, err := queryID(r, "meter_id")
	if err != nil {
		return q, err
	}
	dueFrom, err := queryDate(r, "due_from")
	if err != nil {
		return q, err
	}
	dueTo, err := queryDate(r, "due_to")
	if err != nil {
		return q, err
	}
	q.PropertyID = propertyID
	q.UnitID = unitID
	q.MeterID = meterID
	q.DueFrom = dueFrom
	q.DueTo = dueTo
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

func queryDate(r *http.Request, name string) (time.Time, error) {
	fields := map[string]string{}
	value := parseOptionalDateString(name, r.URL.Query().Get(name), fields)
	if len(fields) > 0 {
		return time.Time{}, httpx.Validation(fields)
	}
	return value, nil
}

// queryFlag reads a boolean filter. Anything that is not an obvious yes is read
// as absent, so ?overdue=0 and no parameter at all behave the same.
func queryFlag(r *http.Request, name string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func pathID(r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}
