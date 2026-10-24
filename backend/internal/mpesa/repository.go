package mpesa

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

// querier lets a repository method run on either the pool or a transaction, which
// is how a webhook can insert its receipt and link the payment in one unit of
// work while a list query keeps using the pool.
type querier interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type repository struct {
	db *sqlx.DB
}

// tenantNameSubquery names the primary tenant of the lease aliased l. A receipt
// is worth looking at only if the accountant can see whose rent it was.
const tenantNameSubquery = `
		(SELECT CONCAT_WS(' ', tu.first_name, tu.last_name)
			FROM lease_tenants lt
			INNER JOIN tenants t ON t.id = lt.tenant_id
			INNER JOIN users tu ON tu.id = t.user_id
			WHERE lt.lease_id = l.id AND lt.role = 'primary'
			ORDER BY lt.tenant_id LIMIT 1)`

// scopeFilter answers "which receipts may this caller see". An M-Pesa row is
// reached through the lease it matched to, so a manager sees the receipts of the
// properties they run and a tenant sees their own. A receipt still in the
// unmatched queue has no lease at all, which leaves it to the staff who reconcile
// (AllProperties) -- a caretaker has nothing to act on there.
func scopeFilter(scope rbac.Scope, leaseColumn string) (string, []any) {
	if scope.AllProperties {
		return "1=1", nil
	}
	var parts []string
	var args []any
	if len(scope.PropertyIDs) > 0 {
		clause, clauseArgs := scope.PropertyFilter("p.id")
		parts = append(parts, clause)
		args = append(args, clauseArgs...)
	}
	if scope.TenantID != nil {
		parts = append(parts, `EXISTS (SELECT 1 FROM lease_tenants lt_own
			WHERE lt_own.lease_id = `+leaseColumn+` AND lt_own.tenant_id = ?)`)
		args = append(args, *scope.TenantID)
	}
	if len(parts) == 0 {
		return "1=0", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

// ---------------------------------------------------------------------------
// Shortcodes. These are organization configuration, not property data, so every
// query filters organization_id and none of them needs a scope.
// ---------------------------------------------------------------------------

const shortcodeColumns = `
	id, organization_id, shortcode, account_type, environment, initiator_name,
	consumer_key_enc, consumer_secret_enc, passkey_enc, security_credential_enc,
	callback_token_hash, callback_token_enc, is_active, created_at, updated_at`

func (r *repository) ListShortcodes(ctx context.Context, orgID uint64, q ShortcodeQuery) ([]ShortcodeRow, int, error) {
	clauses := []string{"organization_id = ?"}
	args := []any{orgID}
	if q.Search != "" {
		clauses = append(clauses, "shortcode LIKE ?")
		args = append(args, likePattern(q.Search))
	}
	if q.Environment != "" {
		clauses = append(clauses, "environment = ?")
		args = append(args, q.Environment)
	}
	if q.AccountType != "" {
		clauses = append(clauses, "account_type = ?")
		args = append(args, q.AccountType)
	}
	if q.Status != "" {
		clauses = append(clauses, "is_active = ?")
		args = append(args, activeFlag(q.Status))
	}
	clause := strings.Join(clauses, " AND ")
	var total int
	if err := r.db.GetContext(ctx, &total, "SELECT COUNT(*) FROM mpesa_shortcodes m WHERE "+clause, args...); err != nil {
		return nil, 0, err
	}
	var rows []ShortcodeRow
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	err := r.db.SelectContext(ctx, &rows,
		"SELECT "+shortcodeColumns+" FROM mpesa_shortcodes m WHERE "+clause+
			" ORDER BY "+orderOr(q.Sort, defaultShortcodeSort)+", m.id DESC LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *repository) Shortcode(ctx context.Context, q querier, orgID, id uint64) (*ShortcodeRow, error) {
	var row ShortcodeRow
	err := q.GetContext(ctx, &row,
		"SELECT "+shortcodeColumns+" FROM mpesa_shortcodes WHERE organization_id = ? AND id = ?", orgID, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ShortcodeByToken resolves the webhook token an organization was handed. The
// lookup is deliberately global: the request carries no domain, no user and no
// organization, only the token, and callback_token_hash is indexed for exactly
// this question.
func (r *repository) ShortcodeByToken(ctx context.Context, tokenHash string) (*ShortcodeRow, error) {
	var row ShortcodeRow
	err := r.db.GetContext(ctx, &row,
		"SELECT "+shortcodeColumns+" FROM mpesa_shortcodes WHERE callback_token_hash = ?", tokenHash)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) InsertShortcode(ctx context.Context, q querier, row *ShortcodeRow) (uint64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO mpesa_shortcodes
			(organization_id, shortcode, account_type, environment, initiator_name,
			 consumer_key_enc, consumer_secret_enc, passkey_enc, security_credential_enc,
			 callback_token_hash, callback_token_enc, is_active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.Shortcode, row.AccountType, row.Environment, row.InitiatorName,
		row.ConsumerKeyEnc, row.ConsumerSecretEnc, row.PasskeyEnc, row.SecurityCredentialEnc,
		row.CallbackTokenHash, row.CallbackTokenEnc, row.IsActive)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) UpdateShortcode(ctx context.Context, q querier, row *ShortcodeRow) error {
	_, err := q.ExecContext(ctx, `
		UPDATE mpesa_shortcodes
		SET shortcode = ?, account_type = ?, environment = ?, initiator_name = ?,
			consumer_key_enc = ?, consumer_secret_enc = ?, passkey_enc = ?,
			security_credential_enc = ?, callback_token_hash = ?, callback_token_enc = ?, is_active = ?
		WHERE organization_id = ? AND id = ?`,
		row.Shortcode, row.AccountType, row.Environment, row.InitiatorName,
		row.ConsumerKeyEnc, row.ConsumerSecretEnc, row.PasskeyEnc, row.SecurityCredentialEnc,
		row.CallbackTokenHash, row.CallbackTokenEnc, row.IsActive, row.OrganizationID, row.ID)
	return err
}

// ShortcodeLinks counts what still points at a shortcode, so deleting one that
// routes live traffic is explained instead of surfacing as a foreign key error.
func (r *repository) ShortcodeLinks(ctx context.Context, q querier, shortcodeID uint64) (refs, transactions, prompts int, err error) {
	err = q.GetContext(ctx, &refs, `SELECT COUNT(*) FROM mpesa_account_refs WHERE shortcode_id = ?`, shortcodeID)
	if err != nil {
		return
	}
	err = q.GetContext(ctx, &transactions, `SELECT COUNT(*) FROM mpesa_transactions WHERE shortcode_id = ?`, shortcodeID)
	if err != nil {
		return
	}
	err = q.GetContext(ctx, &prompts, `SELECT COUNT(*) FROM mpesa_stk_requests WHERE shortcode_id = ?`, shortcodeID)
	return
}

func (r *repository) DeleteShortcode(ctx context.Context, q querier, orgID, id uint64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM mpesa_shortcodes WHERE organization_id = ? AND id = ?`, orgID, id)
	return err
}

// ---------------------------------------------------------------------------
// Account references: the account number a tenant types on a paybill.
// ---------------------------------------------------------------------------

const refFrom = `
	FROM mpesa_account_refs r
	INNER JOIN mpesa_shortcodes m ON m.organization_id = r.organization_id AND m.id = r.shortcode_id
	INNER JOIN leases l ON l.organization_id = r.organization_id AND l.id = r.lease_id
	LEFT JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	LEFT JOIN properties p ON p.organization_id = l.organization_id AND p.id = u.property_id`

const refColumns = `
	r.id, r.organization_id, r.shortcode_id, r.lease_id, r.account_reference, r.is_active,
	r.created_at, r.updated_at, m.shortcode, m.environment, l.lease_number,
	u.number AS unit_number, p.name AS property_name,` + tenantNameSubquery + ` AS tenant_name`

func (r *repository) ListRefs(ctx context.Context, orgID uint64, q RefQuery) ([]AccountRefRow, int, error) {
	clauses := []string{"r.organization_id = ?"}
	args := []any{orgID}
	if q.ShortcodeID > 0 {
		clauses = append(clauses, "r.shortcode_id = ?")
		args = append(args, q.ShortcodeID)
	}
	if q.Search != "" {
		clauses = append(clauses, "(r.account_reference LIKE ? OR l.lease_number LIKE ?)")
		pattern := likePattern(q.Search)
		args = append(args, pattern, pattern)
	}
	clause := strings.Join(clauses, " AND ")
	var total int
	if err := r.db.GetContext(ctx, &total, "SELECT COUNT(*)"+refFrom+" WHERE "+clause, args...); err != nil {
		return nil, 0, err
	}
	var rows []AccountRefRow
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	err := r.db.SelectContext(ctx, &rows,
		"SELECT "+refColumns+refFrom+" WHERE "+clause+
			" ORDER BY "+orderOr(q.Sort, defaultRefSort)+", r.id DESC LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// AccountRefByNumber is how a C2B confirmation becomes a lease. The pair
// (shortcode, account_reference) is unique, so an active row answers the question
// outright; an inactive one is reported as a refusal rather than a miss.
func (r *repository) AccountRefByNumber(ctx context.Context, q querier, orgID, shortcodeID uint64, reference string) (*AccountRefRow, error) {
	var row AccountRefRow
	err := q.GetContext(ctx, &row,
		"SELECT "+refColumns+refFrom+`
		WHERE r.organization_id = ? AND r.shortcode_id = ? AND r.account_reference = ?`,
		orgID, shortcodeID, reference)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) Ref(ctx context.Context, q querier, orgID, id uint64) (*AccountRefRow, error) {
	var row AccountRefRow
	err := q.GetContext(ctx, &row,
		"SELECT "+refColumns+refFrom+" WHERE r.organization_id = ? AND r.id = ?", orgID, id)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) InsertRef(ctx context.Context, q querier, row *AccountRefRow) (uint64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO mpesa_account_refs (organization_id, shortcode_id, lease_id, account_reference, is_active)
		VALUES (?, ?, ?, ?, ?)`,
		row.OrganizationID, row.ShortcodeID, row.LeaseID, row.AccountReference, row.IsActive)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) DeleteRef(ctx context.Context, q querier, orgID, id uint64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM mpesa_account_refs WHERE organization_id = ? AND id = ?`, orgID, id)
	return err
}

// ---------------------------------------------------------------------------
// C2B transactions.
// ---------------------------------------------------------------------------

const transactionFrom = `
	FROM mpesa_transactions mt
	LEFT JOIN leases l ON l.organization_id = mt.organization_id AND l.id = mt.lease_id
	LEFT JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	LEFT JOIN properties p ON p.organization_id = l.organization_id AND p.id = u.property_id
	LEFT JOIN mpesa_shortcodes m ON m.organization_id = mt.organization_id AND m.id = mt.shortcode_id`

const transactionColumns = `
	mt.id, mt.organization_id, mt.trans_id, mt.trans_type, mt.business_short_code, mt.shortcode_id,
	mt.bill_ref_number, mt.lease_id, mt.msisdn, mt.first_name, mt.amount, mt.trans_time, mt.status,
	mt.payment_id, CAST(mt.raw_payload AS CHAR) AS raw_payload, mt.created_at, mt.updated_at,
	l.lease_number, u.number AS unit_number, p.name AS property_name,` + tenantNameSubquery + ` AS tenant_name,
	m.shortcode`

func transactionWhere(orgID uint64, scope rbac.Scope, q TransactionQuery) (string, []any) {
	filter, scopeArgs := scopeFilter(scope, "mt.lease_id")
	clauses := []string{"mt.organization_id = ?", filter}
	args := append([]any{orgID}, scopeArgs...)
	if q.Status != "" {
		clauses = append(clauses, "mt.status = ?")
		args = append(args, q.Status)
	}
	if q.ShortcodeID > 0 {
		clauses = append(clauses, "mt.shortcode_id = ?")
		args = append(args, q.ShortcodeID)
	}
	if q.LeaseID > 0 {
		clauses = append(clauses, "mt.lease_id = ?")
		args = append(args, q.LeaseID)
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "mt.trans_time >= ?")
		args = append(args, q.From)
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "mt.trans_time < ?")
		args = append(args, q.To.AddDate(0, 0, 1))
	}
	if q.Search != "" {
		clauses = append(clauses, "(mt.trans_id LIKE ? OR mt.bill_ref_number LIKE ? OR mt.msisdn LIKE ? OR mt.first_name LIKE ?)")
		pattern := likePattern(q.Search)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	return strings.Join(clauses, " AND "), args
}

func (r *repository) ListTransactions(ctx context.Context, orgID uint64, scope rbac.Scope, q TransactionQuery) ([]TransactionRow, int, error) {
	where, args := transactionWhere(orgID, scope, q)
	var total int
	if err := r.db.GetContext(ctx, &total, "SELECT COUNT(*)"+transactionFrom+" WHERE "+where, args...); err != nil {
		return nil, 0, err
	}
	var rows []TransactionRow
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	err := r.db.SelectContext(ctx, &rows,
		"SELECT "+transactionColumns+transactionFrom+" WHERE "+where+
			" ORDER BY "+orderOr(q.Sort, defaultTransSort)+", mt.id DESC LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *repository) Transaction(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*TransactionRow, error) {
	filter, scopeArgs := scopeFilter(scope, "mt.lease_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row TransactionRow
	err := r.db.GetContext(ctx, &row,
		"SELECT "+transactionColumns+transactionFrom+" WHERE mt.organization_id = ? AND mt.id = ? AND "+filter, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// TransactionByReceipt looks a receipt up by its trans_id, which is unique across
// the whole installation. Safaricom retries a confirmation it did not like the
// look of, and the retry has to find the row the first attempt wrote.
func (r *repository) TransactionByReceipt(ctx context.Context, transID string) (*TransactionRow, error) {
	var row TransactionRow
	err := r.db.GetContext(ctx, &row,
		"SELECT "+transactionColumns+transactionFrom+" WHERE mt.trans_id = ?", transID)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) InsertTransaction(ctx context.Context, q querier, row *TransactionRow) (uint64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO mpesa_transactions
			(organization_id, trans_id, trans_type, business_short_code, shortcode_id, bill_ref_number,
			 lease_id, msisdn, first_name, amount, trans_time, status, payment_id, raw_payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.TransID, row.TransType, row.BusinessShortCode, row.ShortcodeID,
		row.BillRefNumber, row.LeaseID, row.Msisdn, row.FirstName, row.Amount, row.TransTime,
		row.Status, row.PaymentID, rawJSON(row.RawPayload))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// LinkTransaction attaches the receipt to the payment billing booked for it. The
// payment_id IS NULL guard in the caller is what keeps a retry from pointing a
// second payment at the same receipt.
func (r *repository) LinkTransaction(ctx context.Context, q querier, orgID, id, leaseID, paymentID uint64) error {
	_, err := q.ExecContext(ctx, `
		UPDATE mpesa_transactions
		SET lease_id = ?, payment_id = ?, status = ?
		WHERE organization_id = ? AND id = ? AND payment_id IS NULL`,
		optID(leaseID), paymentID, transMatched, orgID, id)
	return err
}

func (r *repository) SetTransactionStatus(ctx context.Context, q querier, orgID, id uint64, status string) error {
	_, err := q.ExecContext(ctx, `
		UPDATE mpesa_transactions SET status = ? WHERE organization_id = ? AND id = ?`,
		status, orgID, id)
	return err
}

// ---------------------------------------------------------------------------
// STK requests.
// ---------------------------------------------------------------------------

const stkFrom = `
	FROM mpesa_stk_requests ms
	LEFT JOIN leases l ON l.organization_id = ms.organization_id AND l.id = ms.lease_id
	LEFT JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	LEFT JOIN properties p ON p.organization_id = l.organization_id AND p.id = u.property_id
	LEFT JOIN mpesa_shortcodes m ON m.organization_id = ms.organization_id AND m.id = ms.shortcode_id
	LEFT JOIN invoices i ON i.organization_id = ms.organization_id AND i.id = ms.invoice_id`

const stkColumns = `
	ms.id, ms.organization_id, ms.shortcode_id, ms.lease_id, ms.invoice_id, ms.phone, ms.amount,
	ms.merchant_request_id, ms.checkout_request_id, ms.status, ms.result_code, ms.result_desc,
	ms.mpesa_receipt, ms.payment_id, ms.created_at, ms.updated_at,
	m.shortcode, m.environment, l.lease_number, u.number AS unit_number, p.name AS property_name,
	i.invoice_number,` + tenantNameSubquery + ` AS tenant_name`

func stkWhere(orgID uint64, scope rbac.Scope, q STKQuery) (string, []any) {
	filter, scopeArgs := scopeFilter(scope, "ms.lease_id")
	clauses := []string{"ms.organization_id = ?", filter}
	args := append([]any{orgID}, scopeArgs...)
	if q.Status != "" {
		clauses = append(clauses, "ms.status = ?")
		args = append(args, q.Status)
	}
	if q.LeaseID > 0 {
		clauses = append(clauses, "ms.lease_id = ?")
		args = append(args, q.LeaseID)
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "ms.created_at >= ?")
		args = append(args, q.From)
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "ms.created_at < ?")
		args = append(args, q.To.AddDate(0, 0, 1))
	}
	if q.Search != "" {
		clauses = append(clauses, "(ms.phone LIKE ? OR ms.checkout_request_id LIKE ? OR ms.mpesa_receipt LIKE ?)")
		pattern := likePattern(q.Search)
		args = append(args, pattern, pattern, pattern)
	}
	return strings.Join(clauses, " AND "), args
}

func (r *repository) ListSTKRequests(ctx context.Context, orgID uint64, scope rbac.Scope, q STKQuery) ([]STKRequestRow, int, error) {
	where, args := stkWhere(orgID, scope, q)
	var total int
	if err := r.db.GetContext(ctx, &total, "SELECT COUNT(*)"+stkFrom+" WHERE "+where, args...); err != nil {
		return nil, 0, err
	}
	var rows []STKRequestRow
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	err := r.db.SelectContext(ctx, &rows,
		"SELECT "+stkColumns+stkFrom+" WHERE "+where+
			" ORDER BY "+orderOr(q.Sort, defaultSTKSort)+", ms.id DESC LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *repository) STKRequest(ctx context.Context, orgID, id uint64, scope rbac.Scope) (*STKRequestRow, error) {
	filter, scopeArgs := scopeFilter(scope, "ms.lease_id")
	args := append([]any{orgID, id}, scopeArgs...)
	var row STKRequestRow
	err := r.db.GetContext(ctx, &row,
		"SELECT "+stkColumns+stkFrom+" WHERE ms.organization_id = ? AND ms.id = ? AND "+filter, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// STKByCheckout finds the prompt a callback answers. CheckoutRequestID is unique
// across organizations, so the callback carries its own organization and the Host
// header is never consulted.
func (r *repository) STKByCheckout(ctx context.Context, checkoutID string) (*STKRequestRow, error) {
	var row STKRequestRow
	err := r.db.GetContext(ctx, &row,
		"SELECT "+stkColumns+stkFrom+" WHERE ms.checkout_request_id = ?", checkoutID)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) InsertSTKRequest(ctx context.Context, q querier, row *STKRequestRow) (uint64, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO mpesa_stk_requests
			(organization_id, shortcode_id, lease_id, invoice_id, phone, amount,
			 merchant_request_id, checkout_request_id, status, result_code, result_desc, mpesa_receipt, payment_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.ShortcodeID, row.LeaseID, row.InvoiceID, row.Phone, row.Amount,
		row.MerchantRequestID, row.CheckoutRequestID, row.Status, row.ResultCode, row.ResultDesc,
		row.MpesaReceipt, row.PaymentID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// RecordSTKResult closes out a prompt. The caller re-reads the row first and
// refuses one that already has a receipt, so a callback Safaricom sends twice
// cannot overwrite a payment that was booked from the first.
func (r *repository) RecordSTKResult(ctx context.Context, q querier, row *STKRequestRow) error {
	_, err := q.ExecContext(ctx, `
		UPDATE mpesa_stk_requests
		SET status = ?, result_code = ?, result_desc = ?, mpesa_receipt = ?, payment_id = ?
		WHERE organization_id = ? AND id = ? AND mpesa_receipt IS NULL`,
		row.Status, row.ResultCode, row.ResultDesc, row.MpesaReceipt, row.PaymentID,
		row.OrganizationID, row.ID)
	return err
}

// ---------------------------------------------------------------------------
// Lookups billing needs but this module owns the decision about.
// ---------------------------------------------------------------------------

// PaymentMethodIDByCode maps a seeded payment method code to its id, so a receipt
// arriving over a till is booked as M-Pesa Till and a prompt as M-Pesa STK.
func (r *repository) PaymentMethodIDByCode(ctx context.Context, code string) (uint64, error) {
	var id uint64
	err := r.db.GetContext(ctx, &id, `SELECT id FROM payment_methods WHERE code = ?`, code)
	return id, err
}

// PaymentByReference finds the payment billing already holds for a receipt number.
// It exists for the retry after a half-finished booking: payments are unique per
// organization, method and reference, so the row a previous attempt wrote can be
// linked instead of paid a second time.
func (r *repository) PaymentByReference(ctx context.Context, orgID, methodID uint64, reference string) (uint64, error) {
	var id uint64
	err := r.db.GetContext(ctx, &id, `
		SELECT id FROM payments
		WHERE organization_id = ? AND payment_method_id = ? AND reference = ?`,
		orgID, methodID, reference)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// AccountRefForLease returns the account number this paybill routes a lease under,
// which is what an STK prompt sends as AccountReference. Passing it is what lets a
// confirmation of the same payment find the lease without a human.
func (r *repository) AccountRefForLease(ctx context.Context, q querier, orgID, shortcodeID, leaseID uint64) (string, error) {
	var reference string
	err := q.GetContext(ctx, &reference, `
		SELECT account_reference FROM mpesa_account_refs
		WHERE organization_id = ? AND shortcode_id = ? AND lease_id = ? AND is_active = 1
		ORDER BY id LIMIT 1`, orgID, shortcodeID, leaseID)
	if err != nil {
		return "", err
	}
	return reference, nil
}

// GatewayLease returns a lease the caller may act on. An out-of-scope lease and a
// missing one are the same answer, so the endpoint does not confirm ids. The scope
// filter is the same one the receipt lists use, so a prompt can only ever be sent
// against a lease the caller is allowed to see.
func (r *repository) GatewayLease(ctx context.Context, q querier, orgID, leaseID uint64, scope rbac.Scope) (*GatewayLeaseRow, error) {
	clause, scopeArgs := scopeFilter(scope, "l.id")
	args := append([]any{orgID, leaseID}, scopeArgs...)
	var row GatewayLeaseRow
	err := q.GetContext(ctx, &row, `
		SELECT l.id, l.organization_id, l.unit_id, u.property_id, l.lease_number, l.status,
			u.number AS unit_number, p.name AS property_name, p.code AS property_code,
			(SELECT CONCAT_WS(' ', tu.first_name, tu.last_name)
				FROM lease_tenants lt INNER JOIN tenants t ON t.id = lt.tenant_id
				INNER JOIN users tu ON tu.id = t.user_id
				WHERE lt.lease_id = l.id AND lt.role = 'primary'
				ORDER BY lt.tenant_id LIMIT 1) AS tenant_name,
			(SELECT tu.phone
				FROM lease_tenants lt INNER JOIN tenants t ON t.id = lt.tenant_id
				INNER JOIN users tu ON tu.id = t.user_id
				WHERE lt.lease_id = l.id AND lt.role = 'primary'
				ORDER BY lt.tenant_id LIMIT 1) AS tenant_phone
		FROM leases l
		INNER JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
		INNER JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id
		WHERE l.organization_id = ? AND l.id = ? AND `+clause, args...)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *repository) GatewayInvoice(ctx context.Context, q querier, orgID, invoiceID uint64) (*GatewayInvoiceRow, error) {
	var row GatewayInvoiceRow
	err := q.GetContext(ctx, &row, `
		SELECT id, lease_id, invoice_number, total, amount_paid, balance, status, due_date
		FROM invoices WHERE organization_id = ? AND id = ?`, orgID, invoiceID)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// rawJSON passes an empty payload as SQL NULL: the column is JSON, and MySQL
// rejects a blank string as surely as it accepts a document.
func rawJSON(value sql.NullString) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return value.String
}

// orderOr keeps a caller's validated ORDER BY and falls back to the module default
// when none was given. httpx.SortParam already refused anything off the whitelist.
func orderOr(order, fallback string) string {
	if strings.TrimSpace(order) == "" {
		return fallback
	}
	return order
}

// activeFlag reads a status filter as the tinyint the column stores.
func activeFlag(status string) int {
	if status == "inactive" {
		return 0
	}
	return 1
}
