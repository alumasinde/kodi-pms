package billing

import (
	"context"
	"sort"
	"strings"

	"kodipms/internal/rbac"
)

// The chart of accounts is seeded by migration 000006. Billing writes into
// everything except owner_payable and management_commission, which belong to
// the owner-money module.
const (
	accountCash               = "cash"
	accountReceivable         = "accounts_receivable"
	accountUnallocated        = "unallocated_funds"
	accountDepositLiability   = "deposit_liability"
	accountTaxPayable         = "tax_payable"
	accountRentIncome         = "rent_income"
	accountLateFeeIncome      = "late_fee_income"
	accountUtilityIncome      = "utility_income"
	accountOtherIncome        = "other_income"
	accountMaintenanceExpense = "maintenance_expense"
	accountIncomeAdjustments  = "income_adjustments"
)

const ledgerTolerance = 0.005

// journalLine is one side of a posting. The ledger table allows exactly one
// non-zero side per row, so a line is always either a debit or a credit.
type journalLine struct {
	AccountCode string
	Debit       float64
	Credit      float64
	Memo        string
}

func debitLine(account string, amount float64, memo string) journalLine {
	return journalLine{AccountCode: account, Debit: roundToCents(amount), Memo: memo}
}

func creditLine(account string, amount float64, memo string) journalLine {
	return journalLine{AccountCode: account, Credit: roundToCents(amount), Memo: memo}
}

// mirror swaps the sides of a posting, which is how a void or a waiver undoes
// an earlier journal instead of rewriting it.
func mirror(lines []journalLine) []journalLine {
	out := make([]journalLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, journalLine{AccountCode: l.AccountCode, Debit: l.Credit, Credit: l.Debit, Memo: l.Memo})
	}
	return out
}

func journalDebits(lines []journalLine) float64 {
	var total float64
	for _, l := range lines {
		total += l.Debit
	}
	return roundToCents(total)
}

func journalCredits(lines []journalLine) float64 {
	var total float64
	for _, l := range lines {
		total += l.Credit
	}
	return roundToCents(total)
}

// journalBalanced is checked in code before anything is written: the triggers
// keep the ledger internally consistent, but only the service knows whether a
// posting is complete.
func journalBalanced(lines []journalLine) bool {
	debits, credits := journalDebits(lines), journalCredits(lines)
	if debits <= 0 {
		return false
	}
	diff := debits - credits
	if diff < 0 {
		diff = -diff
	}
	return diff < ledgerTolerance
}

// incomeAccountFor maps an invoice line to the account that recognises it. A
// deposit is a liability, not income, so it never lands in an income account.
func incomeAccountFor(itemType string) string {
	switch itemType {
	case itemRent:
		return accountRentIncome
	case itemWater, itemElectricity, itemGarbage:
		return accountUtilityIncome
	case itemLateFee:
		return accountLateFeeIncome
	case itemDeposit:
		return accountDepositLiability
	case itemMaintenance:
		return accountMaintenanceExpense
	default:
		return accountOtherIncome
	}
}

// issueLines posts an issued invoice: the whole balance becomes a receivable
// and each income account is credited with the lines of its own kind.
func issueLines(items []InvoiceItemRow, taxTotal, total float64, memo string) []journalLine {
	byAccount := map[string]float64{}
	for _, item := range items {
		if item.Amount <= 0 {
			continue
		}
		byAccount[incomeAccountFor(item.ItemType)] += item.Amount
	}
	accounts := make([]string, 0, len(byAccount))
	for code := range byAccount {
		accounts = append(accounts, code)
	}
	sort.Strings(accounts)

	lines := make([]journalLine, 0, len(accounts)+2)
	lines = append(lines, debitLine(accountReceivable, total, memo))
	for _, code := range accounts {
		amount := roundToCents(byAccount[code])
		if amount > 0 {
			lines = append(lines, creditLine(code, amount, memo))
		}
	}
	if roundToCents(taxTotal) > 0 {
		lines = append(lines, creditLine(accountTaxPayable, taxTotal, memo))
	}
	return lines
}

// paymentLines posts money received: cash in, receivable down by whatever the
// money settled, and any remainder held as tenant credit.
func paymentLines(amount, allocated, credit float64, memo string) []journalLine {
	lines := []journalLine{debitLine(accountCash, amount, memo)}
	if roundToCents(allocated) > 0 {
		lines = append(lines, creditLine(accountReceivable, allocated, memo))
	}
	if roundToCents(credit) > 0 {
		lines = append(lines, creditLine(accountUnallocated, credit, memo))
	}
	return lines
}

// reversalLines unwinds paymentLines for a reversed payment.
func reversalLines(amount, reallocated, credit float64, memo string) []journalLine {
	lines := []journalLine{creditLine(accountCash, amount, memo)}
	if roundToCents(reallocated) > 0 {
		lines = append(lines, debitLine(accountReceivable, reallocated, memo))
	}
	if roundToCents(credit) > 0 {
		lines = append(lines, debitLine(accountUnallocated, credit, memo))
	}
	return lines
}

// applicationLines moves money already received and held on account onto an
// invoice: the liability to the tenant falls, the receivable does too.
func applicationLines(amount float64, memo string) []journalLine {
	return []journalLine{
		debitLine(accountUnallocated, amount, memo),
		creditLine(accountReceivable, amount, memo),
	}
}

// lateFeeLines recognises a late fee as it is added to an invoice.
func lateFeeLines(amount float64, memo string) []journalLine {
	return []journalLine{
		debitLine(accountReceivable, amount, memo),
		creditLine(accountLateFeeIncome, amount, memo),
	}
}

// creditNoteLines reduces what the tenant owes. The pre-tax part is written
// back as an income adjustment and any tax carried by the invoice falls with
// it, so the receivable and the tax payable both stay true to the invoice.
func creditNoteLines(amount, preTax, tax float64, memo string) []journalLine {
	lines := make([]journalLine, 0, 3)
	if roundToCents(preTax) > 0 {
		lines = append(lines, debitLine(accountIncomeAdjustments, preTax, memo))
	}
	if roundToCents(tax) > 0 {
		lines = append(lines, debitLine(accountTaxPayable, tax, memo))
	}
	lines = append(lines, creditLine(accountReceivable, amount, memo))
	return lines
}

// splitPreTax divides a tax-inclusive amount back into the net and tax parts
// of an invoice, so a credit or a waiver keeps subtotal + tax_total = total.
func splitPreTax(subtotal, taxTotal, amount float64) (float64, float64) {
	base := roundToCents(subtotal + taxTotal)
	if base <= 0 {
		return roundToCents(amount), 0
	}
	preTax := roundToCents(amount * roundToCents(subtotal) / base)
	if preTax > roundToCents(amount) {
		preTax = roundToCents(amount)
	}
	return preTax, roundToCents(amount - preTax)
}
func (s *Service) Ledger(ctx context.Context, p *rbac.Principal, q LedgerQuery) (*LedgerListDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	q.AccountCode = strings.ToLower(strings.TrimSpace(q.AccountCode))
	q.SourceType = strings.TrimSpace(q.SourceType)
	rows, total, err := s.repo.ListLedger(ctx, p.OrgID, p.Scope, q)
	if err != nil {
		return nil, err
	}
	items := make([]LedgerEntryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, ledgerViewDTO(&rows[i]))
	}
	return &LedgerListDTO{Entries: items, Total: total}, nil
}

func (s *Service) LedgerAccounts(ctx context.Context, p *rbac.Principal) ([]LedgerAccountDTO, error) {
	if err := authorize(p, "invoices.view"); err != nil {
		return nil, err
	}
	rows, err := s.repo.LedgerAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LedgerAccountDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, LedgerAccountDTO{
			Code: row.Code, Name: row.Name, AccountType: row.AccountType, IsSystem: row.IsSystem != 0,
		})
	}
	return out, nil
}

func ledgerEntryDTOs(rows []LedgerEntryRow) []LedgerEntryDTO {
	out := make([]LedgerEntryDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, ledgerBaseDTO(&row, "", ""))
	}
	return out
}

func ledgerViewDTO(row *LedgerViewRow) LedgerEntryDTO {
	return ledgerBaseDTO(&row.LedgerEntryRow, row.AccountName, row.AccountType)
}

func ledgerBaseDTO(row *LedgerEntryRow, accountName, accountType string) LedgerEntryDTO {
	return LedgerEntryDTO{
		ID: row.ID, JournalID: row.JournalID, EntryDate: row.EntryDate,
		AccountCode: row.AccountCode, AccountName: accountName, AccountType: accountType,
		DebitAmount: row.DebitAmount, CreditAmount: row.CreditAmount,
		CurrencyCode: row.CurrencyCode, SourceType: row.SourceType, SourceID: row.SourceID,
		Memo: nullText(row.Memo), LeaseID: nullIDValue(row.LeaseID),
		CreatedBy: nullIDValue(row.CreatedBy), CreatedAt: row.CreatedAt,
	}
}
