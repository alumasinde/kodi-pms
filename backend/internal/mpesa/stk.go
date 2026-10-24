package mpesa

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"kodipms/internal/audit"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// STK push is this module asking a phone for money, as opposed to C2B, where a
// tenant typed a paybill number without being asked. It is the only outbound call
// this module makes, and it is the reason a shortcode needs credentials at all:
// routing receipts works with nothing but a number.

func (s *Service) STKRequests(ctx context.Context, p *rbac.Principal, q STKQuery) (*STKListDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	if err := normalizeSTKQuery(&q); err != nil {
		return nil, err
	}
	rows, total, err := s.repo.ListSTKRequests(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]STKRequestDTO, 0, len(rows))
	for i := range rows {
		items = append(items, stkDTO(&rows[i]))
	}
	return &STKListDTO{Requests: items, Total: total}, nil
}

func (s *Service) STKRequest(ctx context.Context, p *rbac.Principal, id uint64) (*STKRequestDTO, error) {
	if err := authorize(p, "mpesa.view"); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, httpx.ErrNotFound
	}
	row, err := s.repo.STKRequest(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	out := stkDTO(row)
	return &out, nil
}

// Push prompts a phone for payment.
//
// It fails closed. A prompt needs credentials, a callback token and a live gateway;
// if any is missing, the request is refused with the reason rather than sent and
// left to time out, because a tenant who is charged by a prompt the app cannot
// follow up on is a tenant who has paid into a hole.
//
// Nothing is written until Daraja has answered. A prompt this module never recorded
// still produces a callback it can match by checkout id, while a row for a prompt
// that was never sent teaches the queue to wait for a callback that cannot come.
func (s *Service) Push(ctx context.Context, p *rbac.Principal, in PushInput, meta ClientMeta) (*STKRequestDTO, error) {
	if err := authorize(p, "mpesa.push"); err != nil {
		return nil, err
	}
	fields := map[string]string{}
	if in.ShortcodeID == 0 {
		fields["shortcode_id"] = "required"
	}
	if in.LeaseID == 0 {
		fields["lease_id"] = "required"
	}
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}

	shortcode, err := s.repo.Shortcode(ctx, s.db, p.OrgID, in.ShortcodeID)
	if err != nil {
		return nil, notFound(err)
	}
	if shortcode.IsActive == 0 {
		return nil, httpx.Conflict("This shortcode is deactivated")
	}
	if _, disabled := s.gateway.(disabledGateway); disabled {
		return nil, httpx.Conflict("No M-Pesa gateway is configured on this installation")
	}
	lease, err := s.repo.GatewayLease(ctx, s.db, p.OrgID, in.LeaseID, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	if lease.Status == leaseDraft {
		return nil, httpx.Conflict("A draft lease has no rent owed on it yet")
	}
	creds, err := s.credentials(shortcode)
	if err != nil {
		return nil, err
	}
	token, err := s.callbackToken(shortcode)
	if err != nil {
		return nil, err
	}

	amount, description, err := s.promptAmount(ctx, p, in, lease)
	if err != nil {
		return nil, err
	}
	phone, err := s.promptPhone(in, lease)
	if err != nil {
		return nil, err
	}
	merchantID, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	checkoutID, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	_, _, stkCallback := callbackURLs(s.cfg.PublicBaseURL, token)

	reply, err := s.gateway.STKPush(ctx, shortcode.Environment, creds, STKPushRequest{
		Shortcode:   shortcode.Shortcode,
		AccountType: shortcode.AccountType,
		AccountRef:  s.promptReference(ctx, shortcode, lease),
		Phone:       phone,
		Amount:      amount,
		Description: description,
		MerchantID:  "wx_co_" + merchantID,
		CheckoutID:  "ws_co_" + checkoutID,
		CallbackURL: stkCallback,
	})
	if err != nil {
		s.log.Error("send mpesa stk push", "org_id", p.OrgID, "shortcode", shortcode.Shortcode, "error", err)
		return nil, httpx.Conflict("M-Pesa did not accept the prompt: " + err.Error())
	}
	if reply.ResultCode != callbackAccepted {
		// Daraja refused the request itself. Recording it would put a prompt in the
		// queue that no callback can ever answer.
		return nil, httpx.Conflict("M-Pesa refused the prompt: " + reply.ResultDesc)
	}

	row := &STKRequestRow{
		OrganizationID:    p.OrgID,
		ShortcodeID:       optID(shortcode.ID),
		LeaseID:           optID(in.LeaseID),
		InvoiceID:         optID(in.InvoiceID),
		Phone:             DisplayPhone(phone),
		Amount:            amount,
		MerchantRequestID: optText(reply.MerchantID),
		CheckoutRequestID: firstText(Scalar(reply.CheckoutID), Scalar("ws_co_"+checkoutID)),
		Status:            stkPending,
	}
	id, err := s.repo.InsertSTKRequest(ctx, s.db, row)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, httpx.Conflict("M-Pesa has already been asked to prompt this payment")
		}
		return nil, err
	}

	s.writeAudit(ctx, audit.Entry{
		OrgID: p.OrgID, UserID: p.UserID,
		Action: "mpesa_stk_requests.pushed", EntityType: "mpesa_stk_requests", EntityID: id,
		NewValues: map[string]any{
			"shortcode": shortcode.Shortcode, "lease_id": in.LeaseID, "invoice_id": in.InvoiceID,
			"amount": amount, "phone": row.Phone, "checkout_request_id": row.CheckoutRequestID,
		},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})

	stored, err := s.repo.STKRequest(ctx, p.OrgID, id, p.Scope)
	if err != nil {
		return nil, notFound(err)
	}
	out := stkDTO(stored)
	return &out, nil
}

// ReceiveSTKCallback records what a tenant did with a prompt.
//
// The arbitration rule lives here. A prompt and a C2B confirmation can describe the
// same shilling: a paybill that has confirmation callbacks switched on will send one
// for a payment that started as a prompt. mpesa_transactions is the receipt ledger and
// its trans_id is unique, so the confirmation books the money, this callback finds the
// receipt already booked, and both point at the one payment rather than creating two.
func (s *Service) ReceiveSTKCallback(ctx context.Context, token string, in STKCallbackRequest, raw []byte, meta ClientMeta) CallbackReply {
	body := in.RequestBody
	shortcode, err := s.shortcodeByToken(ctx, token)
	if err != nil {
		return rejectCallback("This paybill is not recognized")
	}
	checkout := strings.TrimSpace(body.CheckoutRequestID.String())
	if checkout == "" {
		return rejectCallback("This callback carries no checkout request id")
	}
	row, err := s.repo.STKByCheckout(ctx, checkout)
	if errors.Is(err, sql.ErrNoRows) {
		// A callback for a prompt this installation never sent. Answering 0 stops
		// Safaricom retrying a request it will never recognize.
		s.log.Warn("stk callback for an unknown prompt", "shortcode", shortcode.Shortcode, "checkout", checkout)
		return acceptCallback()
	}
	if err != nil {
		s.log.Error("look up stk prompt by checkout id", "checkout", checkout, "error", err)
		return rejectCallback("This callback could not be matched, please retry")
	}
	if nullID(row.ShortcodeID) != shortcode.ID || row.OrganizationID != shortcode.OrganizationID {
		s.log.Error("stk callback token does not own this prompt",
			"org_id", shortcode.OrganizationID, "checkout", checkout)
		return rejectCallback("This callback does not belong to this paybill")
	}
	if row.PaymentID.Valid || row.MpesaReceipt.Valid {
		s.log.Info("duplicate stk callback ignored", "org_id", row.OrganizationID, "checkout", checkout)
		return acceptCallback()
	}

	code, codeRead := atoiString(body.ResultCode.String())
	if !codeRead {
		return rejectCallback("This callback carries no result code")
	}
	when, hasTime := channelTime(body.TransactionDate.String())
	if !hasTime {
		when = time.Now().UTC()
	}

	if code != callbackAccepted {
		return s.recordPromptResult(ctx, row, stkStatusForCode(code), code, body.ResultDesc.String(), "", 0, meta)
	}

	receipt := strings.TrimSpace(body.MpesaReceiptNumber.String())
	if receipt == "" {
		s.log.Error("successful stk callback with no receipt", "org_id", row.OrganizationID, "checkout", checkout)
		return rejectCallback("This callback carries no M-Pesa receipt")
	}
	amount, ok := firstAmount(body.Amount)
	if !ok || amount <= 0 {
		amount = roundToCents(row.Amount)
	}
	if amount <= 0 {
		return rejectCallback("The amount could not be read")
	}

	leaseID := nullID(row.LeaseID)
	methodID, err := s.repo.PaymentMethodIDByCode(ctx, methodSTK)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.log.Error("look up stk payment method", "error", err)
		return rejectCallback("This payment cannot be booked right now")
	}

	booking := receiptBooking{
		OrganizationID:    row.OrganizationID,
		ShortcodeID:       nullID(row.ShortcodeID),
		TransID:           receipt,
		TransType:         "STK Push",
		BusinessShortCode: shortcode.Shortcode,
		Msisdn:            s.receiptPhone(row.Phone),
		LeaseID:           leaseID,
		Amount:            amount,
		TransTime:         when,
		PaymentMethodID:   methodID,
		Notes:             "STK prompt " + checkout,
		RawPayload:        string(raw),
	}
	outcome, err := s.recordReceipt(ctx, booking, meta)
	if err != nil {
		s.log.Error("record stk payment", "org_id", row.OrganizationID, "receipt", receipt, "error", err)
		return rejectCallback("This payment could not be recorded, please retry")
	}
	if outcome.ForeignReceipt {
		return rejectCallback("This receipt already belongs to another account")
	}
	status := stkSuccess
	paymentID := uint64(0)
	if outcome.Payment != nil {
		paymentID = outcome.Payment.PaymentID
	}
	// The prompt succeeded the moment the tenant paid, even if the receipt has no
	// lease to sit on yet. That is the queue's problem, not the prompt's, and
	// calling it failed here would hide a payment the tenant really made.
	return s.recordPromptResult(ctx, row, status, code, body.ResultDesc.String(), receipt, paymentID, meta)
}

// recordPromptResult closes a prompt out. RecordSTKResult only writes a row that has
// no receipt yet, so a callback Safaricom sends twice cannot move a payment that the
// first one already placed.
func (s *Service) recordPromptResult(ctx context.Context, row *STKRequestRow, status string, code int, desc, receipt string, paymentID uint64, meta ClientMeta) CallbackReply {
	update := &STKRequestRow{
		ID:             row.ID,
		OrganizationID: row.OrganizationID,
		Status:         status,
		ResultCode:     sql.NullInt64{Int64: int64(code), Valid: true},
		ResultDesc:     optText(truncate(desc, maxResultDesc)),
		MpesaReceipt:   optText(truncate(receipt, 30)),
		PaymentID:      optID(paymentID),
	}
	if err := s.repo.RecordSTKResult(ctx, s.db, update); err != nil {
		s.log.Error("record stk result", "org_id", row.OrganizationID, "stk_id", row.ID, "error", err)
		return rejectCallback("This result could not be saved, please retry")
	}
	s.writeAudit(ctx, audit.Entry{
		OrgID:  row.OrganizationID,
		Action: "mpesa_stk_requests." + status, EntityType: "mpesa_stk_requests", EntityID: row.ID,
		NewValues: map[string]any{"status": status, "result_code": code, "receipt": receipt,
			"payment_id": paymentID, "checkout_request_id": row.CheckoutRequestID},
		IPAddress: meta.IP, UserAgent: meta.UserAgent,
	})
	return acceptCallback()
}

// promptAmount decides what to ask for. An explicit amount wins; otherwise an
// invoice offers its own balance, which is the number that changes as other payments
// land, so a prompt written yesterday cannot be sent for a stale figure.
func (s *Service) promptAmount(ctx context.Context, p *rbac.Principal, in PushInput, lease *GatewayLeaseRow) (float64, string, error) {
	if in.Amount != nil {
		amount := roundToCents(*in.Amount)
		if amount <= 0 || amount > maxAmount {
			return 0, "", httpx.Validation(map[string]string{"amount": "must be between 0.01 and 9999999999.99"})
		}
		return amount, promptDescription(in.Description, defaultPrompt(lease)), nil
	}
	if in.InvoiceID == 0 {
		return 0, "", httpx.Validation(map[string]string{
			"amount": "required without an invoice to take its balance from",
		})
	}
	invoice, err := s.repo.GatewayInvoice(ctx, s.db, p.OrgID, in.InvoiceID)
	if err != nil {
		return 0, "", notFound(err)
	}
	if lease.ID != invoice.LeaseID {
		return 0, "", httpx.Validation(map[string]string{"invoice_id": "belongs to another lease"})
	}
	balance := roundToCents(invoice.Balance)
	if balance <= 0 {
		return 0, "", httpx.Conflict("Invoice " + invoice.InvoiceNumber + " has nothing left owing on it")
	}
	return balance, promptDescription(in.Description, "Payment for invoice "+invoice.InvoiceNumber), nil
}

// promptPhone defaults to the primary tenant's line, so a receivable can be chased
// without a clerk looking it up first. A phone given explicitly is the caller's, and
// is still normalized: Daraja rejects a number that starts with a zero.
func (s *Service) promptPhone(in PushInput, lease *GatewayLeaseRow) (string, error) {
	candidate := ""
	if in.Phone != nil {
		candidate = strings.TrimSpace(*in.Phone)
	} else if lease.TenantPhone.Valid {
		candidate = lease.TenantPhone.String
	}
	if candidate == "" {
		return "", httpx.Validation(map[string]string{"phone": "required, and this lease has no tenant phone"})
	}
	normalized, err := normalizeMsisdn(candidate)
	if err != nil {
		return "", httpx.Validation(map[string]string{"phone": "is not a Kenyan mobile number"})
	}
	return normalized, nil
}

// promptReference is the account number the prompt tells the tenant to pay against.
// Using the lease's own reference is what makes a later C2B confirmation of the same
// money find the lease without anybody typing anything.
func (s *Service) promptReference(ctx context.Context, shortcode *ShortcodeRow, lease *GatewayLeaseRow) string {
	if ref, err := s.repo.AccountRefForLease(ctx, s.db, shortcode.OrganizationID, shortcode.ID, lease.ID); err == nil && ref != "" {
		return truncate(ref, maxAccountRef)
	}
	return truncate(firstText(Scalar(lease.LeaseNumber), Scalar("KODI")), maxAccountRef)
}

func promptDescription(value *string, fallback string) string {
	if value != nil && strings.TrimSpace(*value) != "" {
		return truncate(*value, maxDescription)
	}
	return truncate(fallback, maxDescription)
}

func defaultPrompt(lease *GatewayLeaseRow) string {
	return "Rent for " + lease.UnitNumber + " at " + lease.PropertyName
}

// stkStatusForCode names what happened in the words the queue shows. Daraja's own
// codes say whether the tenant hung up, took too long, or was refused by their own
// account, and those are different facts for whoever chases the rent.
func stkStatusForCode(code int) string {
	switch code {
	case resultCancelled:
		return stkCancelled
	case resultTimeout:
		return stkTimeout
	default:
		return stkFailed
	}
}

func atoiString(value string) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, false
	}
	return n, true
}

func normalizeSTKQuery(q *STKQuery) error {
	fields := map[string]string{}
	q.Search = truncate(q.Search, maxSearch)
	if q.Status != "" && !inList(q.Status, stkStatuses) {
		fields["status"] = stkStatusMessage
	}
	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

func stkDTO(row *STKRequestRow) STKRequestDTO {
	return STKRequestDTO{
		ID:                row.ID,
		ShortcodeID:       nullID(row.ShortcodeID),
		Shortcode:         nullText(row.Shortcode),
		Environment:       nullText(row.Environment),
		LeaseID:           nullID(row.LeaseID),
		LeaseNumber:       nullText(row.LeaseNumber),
		UnitNumber:        nullText(row.UnitNumber),
		PropertyName:      nullText(row.PropertyName),
		TenantName:        nullText(row.TenantName),
		InvoiceID:         nullID(row.InvoiceID),
		InvoiceNumber:     nullText(row.InvoiceNumber),
		Phone:             row.Phone,
		Amount:            roundToCents(row.Amount),
		MerchantRequestID: nullText(row.MerchantRequestID),
		CheckoutRequestID: row.CheckoutRequestID,
		Status:            row.Status,
		ResultCode:        intPtr(row.ResultCode),
		ResultDesc:        nullText(row.ResultDesc),
		MpesaReceipt:      nullText(row.MpesaReceipt),
		PaymentID:         nullID(row.PaymentID),
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}
