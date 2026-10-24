-- Real billing ledger, tenant credit, credit notes and payment reversals.
--
-- Invoices.amount_paid is now derived from live payment allocations:
--   * an allocation cannot exceed the payment amount;
--   * an allocation must belong to the payment's lease;
--   * allocation rows are immutable apart from reversed_at;
--   * every allocation change re-syncs invoices.amount_paid.
-- The billing service posts balanced rows into ledger_entries for each money
-- event; ledger_entries and tenant_credit_entries are append-only.

CREATE TABLE IF NOT EXISTS ledger_accounts (
  code         VARCHAR(40)  NOT NULL,
  name         VARCHAR(100) NOT NULL,
  account_type VARCHAR(20)  NOT NULL,
  is_system    TINYINT(1)   NOT NULL DEFAULT 1,
  created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (code),
  CONSTRAINT chk_ledger_accounts_type
    CHECK (account_type IN ('asset','liability','equity','income','expense'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO ledger_accounts (code, name, account_type) VALUES
  ('cash',                 'Cash and bank',                    'asset'),
  ('accounts_receivable',  'Tenant receivables',               'asset'),
  ('unallocated_funds',    'Unallocated tenant funds / credit','liability'),
  ('deposit_liability',    'Tenant deposits held',             'liability'),
  ('owner_payable',        'Owner payable',                    'liability'),
  ('tax_payable',          'Tax payable',                      'liability'),
  ('rent_income',          'Rent income',                      'income'),
  ('late_fee_income',      'Late fee income',                  'income'),
  ('utility_income',       'Utility income',                   'income'),
  ('other_income',         'Other income',                     'income'),
  ('management_commission','Management commission income',     'income'),
  ('maintenance_expense',  'Maintenance expense',              'expense'),
  ('income_adjustments',   'Credit notes and income adjustments','expense');

CREATE TABLE IF NOT EXISTS ledger_entries (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  lease_id        BIGINT UNSIGNED NULL,
  journal_id      CHAR(36)        NOT NULL COMMENT 'Groups the debit/credit lines of one posting',
  entry_date      DATE            NOT NULL,
  account_code    VARCHAR(40)     NOT NULL,
  debit_amount    DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  credit_amount   DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  currency_code   CHAR(3)         NOT NULL DEFAULT 'KES',
  source_type     VARCHAR(40)     NOT NULL COMMENT 'invoice, payment, allocation, reversal, credit_note, owner_statement',
  source_id       BIGINT UNSIGNED NOT NULL,
  memo            VARCHAR(255)    NULL,
  created_by      BIGINT UNSIGNED NULL,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_ledger_org_date (organization_id, entry_date),
  KEY idx_ledger_lease (organization_id, lease_id),
  KEY idx_ledger_source (source_type, source_id),
  KEY idx_ledger_journal (journal_id),
  CONSTRAINT fk_ledger_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_ledger_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_ledger_account
    FOREIGN KEY (account_code) REFERENCES ledger_accounts (code) ON DELETE RESTRICT,
  CONSTRAINT chk_ledger_sides
    CHECK ((debit_amount > 0 AND credit_amount = 0)
        OR (credit_amount > 0 AND debit_amount = 0))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TRIGGER trg_ledger_entries_no_update BEFORE UPDATE ON ledger_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'ledger_entries is append-only';

CREATE TRIGGER trg_ledger_entries_no_delete BEFORE DELETE ON ledger_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'ledger_entries is append-only';

CREATE TABLE IF NOT EXISTS credit_notes (
  id                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id    BIGINT UNSIGNED NOT NULL,
  lease_id           BIGINT UNSIGNED NOT NULL,
  invoice_id         BIGINT UNSIGNED NULL,
  credit_note_number VARCHAR(50)     NOT NULL,
  amount             DECIMAL(12,2)   NOT NULL,
  reason             TEXT            NULL,
  status             VARCHAR(20)     NOT NULL DEFAULT 'draft',
  issue_date         DATE            NOT NULL,
  applied_at         DATETIME        NULL,
  created_by         BIGINT UNSIGNED NULL,
  created_at         DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_credit_notes_org_number (organization_id, credit_note_number),
  KEY idx_credit_notes_org_lease (organization_id, lease_id),
  KEY idx_credit_notes_org_invoice (organization_id, invoice_id),
  CONSTRAINT fk_credit_notes_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_credit_notes_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_credit_notes_invoice
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT chk_credit_notes_amount CHECK (amount > 0),
  CONSTRAINT chk_credit_notes_status
    CHECK (status IN ('draft','issued','applied','void'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- An issued credit note is immutable; it can only move to applied/void.
CREATE TRIGGER trg_credit_notes_issued_immutable BEFORE UPDATE ON credit_notes
FOR EACH ROW
BEGIN
  IF OLD.status <> 'draft' AND (
       NEW.organization_id <> OLD.organization_id
    OR NEW.lease_id <> OLD.lease_id
    OR (NEW.invoice_id IS NULL) <> (OLD.invoice_id IS NULL)
    OR (NEW.invoice_id IS NOT NULL AND NEW.invoice_id <> OLD.invoice_id)
    OR NEW.amount <> OLD.amount
    OR NEW.issue_date <> OLD.issue_date
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'An issued credit note cannot be changed';
  END IF;
  IF OLD.status IN ('applied','void') AND NEW.status <> OLD.status THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'A finalised credit note cannot change status';
  END IF;
END;

CREATE TABLE IF NOT EXISTS tenant_credit_entries (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  lease_id        BIGINT UNSIGNED NULL,
  tenant_id       BIGINT UNSIGNED NULL,
  invoice_id      BIGINT UNSIGNED NULL,
  entry_type      VARCHAR(30)     NOT NULL,
  amount          DECIMAL(12,2)   NOT NULL COMMENT 'positive adds tenant credit, negative consumes it',
  source_type     VARCHAR(40)     NULL,
  source_id       BIGINT UNSIGNED NULL,
  notes           VARCHAR(255)    NULL,
  created_by      BIGINT UNSIGNED NULL,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_tenant_credit_org_lease (organization_id, lease_id, created_at),
  KEY idx_tenant_credit_org_tenant (organization_id, tenant_id, created_at),
  KEY idx_tenant_credit_org_invoice (organization_id, invoice_id),
  CONSTRAINT fk_tenant_credit_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_tenant_credit_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_tenant_credit_tenant
    FOREIGN KEY (organization_id, tenant_id) REFERENCES tenants (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_tenant_credit_invoice
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT chk_tenant_credit_amount CHECK (amount <> 0),
  CONSTRAINT chk_tenant_credit_subject CHECK (lease_id IS NOT NULL OR tenant_id IS NOT NULL),
  CONSTRAINT chk_tenant_credit_type
    CHECK (entry_type IN ('overpayment','credit_note','application','refund','adjustment','reversal'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TRIGGER trg_tenant_credit_no_update BEFORE UPDATE ON tenant_credit_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'tenant_credit_entries is append-only';

CREATE TRIGGER trg_tenant_credit_no_delete BEFORE DELETE ON tenant_credit_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'tenant_credit_entries is append-only';

-- ---------------------------------------------------------------------------
-- Payment reversals. A reversal is full-payment, one per payment, and keeps
-- history by marking allocations reversed instead of deleting them.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS payment_reversals (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  payment_id      BIGINT UNSIGNED NOT NULL,
  amount          DECIMAL(12,2)   NOT NULL,
  reason          VARCHAR(255)    NULL,
  reversed_by     BIGINT UNSIGNED NULL,
  reversed_at     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_payment_reversals_payment (payment_id),
  KEY idx_payment_reversals_org (organization_id, reversed_at),
  CONSTRAINT fk_reversals_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_reversals_payment
    FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_reversals_user
    FOREIGN KEY (reversed_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_reversals_amount CHECK (amount > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TRIGGER trg_payment_reversals_validate BEFORE INSERT ON payment_reversals
FOR EACH ROW
BEGIN
  DECLARE pay_amount DECIMAL(12,2);
  DECLARE pay_status VARCHAR(30);
  SELECT amount, status INTO pay_amount, pay_status
  FROM payments WHERE id = NEW.payment_id AND organization_id = NEW.organization_id;
  IF pay_amount IS NULL THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Payment does not exist in this organization';
  END IF;
  IF pay_status <> 'completed' THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Only a completed payment can be reversed';
  END IF;
  IF NEW.amount <> pay_amount THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'A payment reversal must reverse the full payment amount';
  END IF;
END;

CREATE TRIGGER trg_payment_reversals_apply AFTER INSERT ON payment_reversals
FOR EACH ROW
BEGIN
  UPDATE payment_allocations
  SET reversed_at = NEW.reversed_at
  WHERE payment_id = NEW.payment_id AND reversed_at IS NULL;
  UPDATE payments SET status = 'reversed' WHERE id = NEW.payment_id;
END;

-- ---------------------------------------------------------------------------
-- Allocation integrity and derived invoice.amount_paid.
-- ---------------------------------------------------------------------------
ALTER TABLE payment_allocations
  ADD COLUMN reversed_at DATETIME NULL AFTER amount;

-- Bring amount_paid back in line with what is actually allocated before the
-- triggers start maintaining it.
UPDATE invoices i
SET i.amount_paid = (
  SELECT COALESCE(SUM(pa.amount), 0)
  FROM payment_allocations pa
  WHERE pa.invoice_id = i.id AND pa.reversed_at IS NULL
);

-- amount_paid is derived. New invoices start at zero; updates must match the
-- live allocation sum, which is exactly what the sync triggers below write.
CREATE TRIGGER trg_invoices_amount_paid_ins BEFORE INSERT ON invoices
FOR EACH ROW
BEGIN
  IF NEW.amount_paid <> 0 THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'invoices.amount_paid is derived from payment allocations';
  END IF;
END;

CREATE TRIGGER trg_invoices_amount_paid_upd BEFORE UPDATE ON invoices
FOR EACH ROW
BEGIN
  DECLARE allocated DECIMAL(12,2);
  SELECT COALESCE(SUM(amount), 0) INTO allocated
  FROM payment_allocations
  WHERE invoice_id = NEW.id AND reversed_at IS NULL;
  IF NEW.amount_paid <> allocated THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'invoices.amount_paid is derived from payment allocations';
  END IF;
END;

CREATE TRIGGER trg_allocations_validate BEFORE INSERT ON payment_allocations
FOR EACH ROW
BEGIN
  DECLARE pay_amount   DECIMAL(12,2);
  DECLARE pay_lease    BIGINT UNSIGNED;
  DECLARE pay_status   VARCHAR(30);
  DECLARE invoice_lease BIGINT UNSIGNED;
  DECLARE invoice_total DECIMAL(12,2);
  DECLARE invoice_paid  DECIMAL(12,2);
  DECLARE allocated    DECIMAL(12,2);

  SELECT amount, lease_id, status INTO pay_amount, pay_lease, pay_status
  FROM payments WHERE id = NEW.payment_id;
  IF pay_amount IS NULL THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Payment does not exist';
  END IF;
  IF pay_status <> 'completed' THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Allocations require a completed payment';
  END IF;

  SELECT lease_id, total, amount_paid INTO invoice_lease, invoice_total, invoice_paid
  FROM invoices WHERE id = NEW.invoice_id;
  IF invoice_lease IS NULL OR invoice_lease <> pay_lease THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Payment and invoice must belong to the same lease';
  END IF;
  IF invoice_paid + NEW.amount > invoice_total THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'Allocation exceeds the invoice balance; keep the excess as tenant credit';
  END IF;

  SELECT COALESCE(SUM(amount), 0) INTO allocated
  FROM payment_allocations
  WHERE payment_id = NEW.payment_id AND reversed_at IS NULL;
  IF allocated + NEW.amount > pay_amount THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Allocations cannot exceed the payment amount';
  END IF;
END;

CREATE TRIGGER trg_allocations_immutable BEFORE UPDATE ON payment_allocations
FOR EACH ROW
BEGIN
  IF NEW.organization_id <> OLD.organization_id
     OR NEW.payment_id <> OLD.payment_id
     OR NEW.invoice_id <> OLD.invoice_id
     OR NEW.amount <> OLD.amount THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'Allocation fields are immutable; reverse the payment instead';
  END IF;
  IF OLD.reversed_at IS NOT NULL AND NEW.reversed_at IS NULL THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'An allocation reversal cannot be undone';
  END IF;
END;

CREATE TRIGGER trg_allocations_no_delete BEFORE DELETE ON payment_allocations
FOR EACH ROW SIGNAL SQLSTATE '45000'
  SET MESSAGE_TEXT = 'Payment allocations cannot be deleted; reverse the payment instead';

CREATE TRIGGER trg_allocations_sync_invoice_ins AFTER INSERT ON payment_allocations
FOR EACH ROW
UPDATE invoices
SET amount_paid = (
  SELECT COALESCE(SUM(amount), 0)
  FROM payment_allocations
  WHERE invoice_id = NEW.invoice_id AND reversed_at IS NULL
)
WHERE id = NEW.invoice_id;

CREATE TRIGGER trg_allocations_sync_invoice_upd AFTER UPDATE ON payment_allocations
FOR EACH ROW
UPDATE invoices
SET amount_paid = (
  SELECT COALESCE(SUM(amount), 0)
  FROM payment_allocations
  WHERE invoice_id = NEW.invoice_id AND reversed_at IS NULL
)
WHERE id = NEW.invoice_id;

-- Payments cannot silently shrink below what is already allocated, cannot be
-- moved to another lease/organization, and can only be marked reversed through
-- a payment_reversals row.
CREATE TRIGGER trg_payments_validate BEFORE UPDATE ON payments
FOR EACH ROW
BEGIN
  DECLARE allocated DECIMAL(12,2);
  SELECT COALESCE(SUM(amount), 0) INTO allocated
  FROM payment_allocations
  WHERE payment_id = NEW.id AND reversed_at IS NULL;
  IF NEW.organization_id <> OLD.organization_id OR NEW.lease_id <> OLD.lease_id THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Payment organization and lease are immutable';
  END IF;
  IF NEW.amount < allocated THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Payment amount cannot be below its live allocations';
  END IF;
  IF NEW.status = 'reversed' AND OLD.status <> 'reversed'
     AND NOT EXISTS (SELECT 1 FROM payment_reversals WHERE payment_id = NEW.id) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Use payment_reversals to reverse a payment';
  END IF;
  IF OLD.status = 'reversed' AND NEW.status <> 'reversed' THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'A reversed payment cannot be reopened';
  END IF;
END;

CREATE TRIGGER trg_payments_no_delete BEFORE DELETE ON payments
FOR EACH ROW SIGNAL SQLSTATE '45000'
  SET MESSAGE_TEXT = 'Payments cannot be deleted; use a payment reversal';
