DROP TRIGGER IF EXISTS trg_payments_no_delete;
DROP TRIGGER IF EXISTS trg_payments_validate;
DROP TRIGGER IF EXISTS trg_allocations_sync_invoice_upd;
DROP TRIGGER IF EXISTS trg_allocations_sync_invoice_ins;
DROP TRIGGER IF EXISTS trg_allocations_no_delete;
DROP TRIGGER IF EXISTS trg_allocations_immutable;
DROP TRIGGER IF EXISTS trg_allocations_validate;
DROP TRIGGER IF EXISTS trg_invoices_amount_paid_upd;
DROP TRIGGER IF EXISTS trg_invoices_amount_paid_ins;

ALTER TABLE payment_allocations DROP COLUMN reversed_at;

DROP TABLE IF EXISTS payment_reversals;
DROP TABLE IF EXISTS tenant_credit_entries;
DROP TABLE IF EXISTS credit_notes;
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS ledger_accounts;
