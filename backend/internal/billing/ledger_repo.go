package billing

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"kodipms/internal/rbac"
)

const ledgerColumns = `
	le.id, le.organization_id, le.lease_id, le.journal_id, le.entry_date, le.account_code,
	le.debit_amount, le.credit_amount, le.currency_code, le.source_type, le.source_id,
	le.memo, le.created_by, le.created_at`

// ledgerViewQuery reuses the tenant subquery with the ledger's own lease
// column, so it has to be built at run time rather than as a constant.
var ledgerViewQuery = `
	SELECT ` + ledgerColumns + `,
		la.name AS account_name, la.account_type,
		l.lease_number, u.number AS unit_number, COALESCE(u.property_id, 0) AS property_id,
		p.name AS property_name,` + strings.ReplaceAll(tenantNameSubquery, "lt.lease_id = l.id", "lt.lease_id = le.lease_id") + ` AS primary_tenant
	FROM ledger_entries le
	INNER JOIN ledger_accounts la ON la.code = le.account_code
	LEFT JOIN leases l ON l.organization_id = le.organization_id AND l.id = le.lease_id
	LEFT JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	LEFT JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id`

// PostJournal writes one balanced double-entry posting and returns the journal
// id that groups it. An unbalanced journal is a bug in the caller, so it is
// refused before anything reaches the database.
func (r *repository) PostJournal(ctx context.Context, tx *sqlx.Tx, orgID, leaseID uint64, sourceType string, sourceID uint64, entryDate time.Time, currency string, createdBy uint64, lines []journalLine) (string, error) {
	if len(lines) == 0 || !journalBalanced(lines) {
		return "", errUnbalancedJournal
	}
	journalID := uuid.NewString()
	for _, line := range lines {
		if line.Debit <= 0 && line.Credit <= 0 {
			continue
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO ledger_entries
				(organization_id, lease_id, journal_id, entry_date, account_code, debit_amount, credit_amount,
				 currency_code, source_type, source_id, memo, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, nullableID(leaseID), journalID, entryDate, line.AccountCode, line.Debit, line.Credit,
			currency, sourceType, sourceID, truncate(line.Memo, 255), nullableID(createdBy))
		if err != nil {
			return "", err
		}
	}
	return journalID, nil
}

func (r *repository) ListLedger(ctx context.Context, orgID uint64, scope rbac.Scope, q LedgerQuery) ([]LedgerViewRow, int, error) {
	where, args := ledgerWhere(orgID, scope, q)
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+ledgerFrom+` WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	order := q.Sort
	if order == "" {
		order = defaultLedgerSort
	}
	listArgs := append(append([]any{}, args...), q.Page.Limit, q.Page.Offset)
	var rows []LedgerViewRow
	err := r.db.SelectContext(ctx, &rows, ledgerViewQuery+` WHERE `+where+` ORDER BY `+order+`, le.id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

const ledgerFrom = `
	FROM ledger_entries le
	INNER JOIN ledger_accounts la ON la.code = le.account_code
	LEFT JOIN leases l ON l.organization_id = le.organization_id AND l.id = le.lease_id
	LEFT JOIN units u ON u.organization_id = l.organization_id AND u.id = l.unit_id
	LEFT JOIN properties p ON p.organization_id = u.organization_id AND p.id = u.property_id`

func ledgerWhere(orgID uint64, scope rbac.Scope, q LedgerQuery) (string, []any) {
	// A posting with no lease is an organization-level entry: only a caller who
	// can see the whole organization gets it.
	where := []string{"le.organization_id = ?"}
	args := []any{orgID}
	if !scope.AllProperties {
		clause, scopeArgs := scopeFilter(scope, "u.property_id", "le.lease_id")
		where = append(where, "(le.lease_id IS NOT NULL AND "+clause+")")
		args = append(args, scopeArgs...)
	}
	if q.AccountCode != "" {
		where = append(where, "le.account_code = ?")
		args = append(args, q.AccountCode)
	}
	if q.SourceType != "" {
		where = append(where, "le.source_type = ?")
		args = append(args, q.SourceType)
	}
	if q.SourceID != 0 {
		where = append(where, "le.source_id = ?")
		args = append(args, q.SourceID)
	}
	if q.LeaseID != 0 {
		where = append(where, "le.lease_id = ?")
		args = append(args, q.LeaseID)
	}
	if q.PropertyID != 0 {
		where = append(where, "u.property_id = ?")
		args = append(args, q.PropertyID)
	}
	if !q.From.IsZero() {
		where = append(where, "le.entry_date >= ?")
		args = append(args, q.From)
	}
	if !q.To.IsZero() {
		where = append(where, "le.entry_date <= ?")
		args = append(args, q.To)
	}
	return strings.Join(where, " AND "), args
}

func (r *repository) LedgerForSource(ctx context.Context, orgID uint64, sourceType string, sourceID uint64) ([]LedgerEntryRow, error) {
	var rows []LedgerEntryRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT `+ledgerColumns+`
		FROM ledger_entries le
		WHERE le.organization_id = ? AND le.source_type = ? AND le.source_id = ?
		ORDER BY le.id`, orgID, sourceType, sourceID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) LedgerAccounts(ctx context.Context) ([]LedgerAccountRow, error) {
	var rows []LedgerAccountRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT code, name, account_type, is_system FROM ledger_accounts ORDER BY account_type, code`)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repository) InsertTenantCredit(ctx context.Context, tx *sqlx.Tx, row *TenantCreditRow) (uint64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO tenant_credit_entries
			(organization_id, lease_id, tenant_id, invoice_id, entry_type, amount, source_type, source_id, notes, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.OrganizationID, row.LeaseID, row.TenantID, row.InvoiceID,
		row.EntryType, row.Amount, row.SourceType, row.SourceID, row.Notes, row.CreatedBy)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (r *repository) TenantCreditEntries(ctx context.Context, orgID, leaseID uint64) ([]TenantCreditRow, error) {
	var rows []TenantCreditRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, organization_id, lease_id, tenant_id, invoice_id, entry_type, amount,
			source_type, source_id, notes, created_by, created_at
		FROM tenant_credit_entries WHERE organization_id = ? AND lease_id = ? ORDER BY id`, orgID, leaseID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
