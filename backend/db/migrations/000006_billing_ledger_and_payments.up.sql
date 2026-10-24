-- Invoices, line items, payments and their allocation to invoices, late fees,
-- the double-entry ledger, tenant credit, payment reversals and owner money.
--
-- The database enforces the money invariants:
--   * invoices.amount_paid is derived from live payment allocations;
--   * an allocation cannot exceed the payment or the invoice balance;
--   * allocations are immutable (reverse the payment instead);
--   * payments cannot be deleted and a reversal is a full-payment row;
--   * ledger_entries and tenant_credit_entries are append-only.
-- The billing service still has to post balanced journal rows for each event;
-- the triggers enforce consistency, not double-entry completeness.
CREATE TABLE IF NOT EXISTS payment_methods (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  code        VARCHAR(30)  NOT NULL,
  name        VARCHAR(50)  NOT NULL,
  description VARCHAR(255) NULL,
  is_active   TINYINT(1) NOT NULL DEFAULT 1,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_payment_methods_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS invoices (
  id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id      BIGINT UNSIGNED NOT NULL,
  lease_id             BIGINT UNSIGNED NOT NULL,
  invoice_number       VARCHAR(50) NOT NULL,
  invoice_type         VARCHAR(30) NOT NULL DEFAULT 'rent',
  billing_period_start DATE NOT NULL,
  billing_period_end   DATE NOT NULL,
  issue_date           DATE NOT NULL,
  due_date             DATE NOT NULL,
  subtotal             DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  tax_total            DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  total                DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  amount_paid          DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  balance              DECIMAL(12,2) GENERATED ALWAYS AS (total - amount_paid) STORED,
  status               VARCHAR(30) NOT NULL DEFAULT 'draft',
  notes                TEXT NULL,
  created_by           BIGINT UNSIGNED NULL,
  created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_invoices_org_number (organization_id, invoice_number),
  UNIQUE KEY uq_invoices_org_id (organization_id, id),
  UNIQUE KEY uq_invoices_lease_period (lease_id, invoice_type, billing_period_start),
  KEY idx_invoices_org_lease (organization_id, lease_id),
  KEY idx_invoices_creator (created_by),
  KEY idx_invoices_due (due_date),
  KEY idx_invoices_status (status),
  CONSTRAINT fk_invoices_org       FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_invoices_lease_org FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT fk_invoices_creator   FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_invoices_amounts  CHECK (total >= 0 AND amount_paid >= 0 AND amount_paid <= total),
  CONSTRAINT chk_invoices_status   CHECK (status IN ('draft','issued','partially_paid','paid','overdue','void'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS invoice_items (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  invoice_id      BIGINT UNSIGNED NOT NULL,
  item_type       VARCHAR(30)  NOT NULL DEFAULT 'rent',
  description     VARCHAR(255) NOT NULL,
  quantity        DECIMAL(12,3) NOT NULL DEFAULT 1.000,
  unit_price      DECIMAL(12,2) NOT NULL,
  tax_rate        DECIMAL(5,2)  NOT NULL DEFAULT 0.00,
  amount          DECIMAL(12,2) NOT NULL,
  source_type     VARCHAR(50) NULL COMMENT 'e.g. utility_bill, late_fee, maintenance_expense',
  source_id       BIGINT UNSIGNED NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_invoice_items_invoice (invoice_id),
  KEY idx_items_org_invoice (organization_id, invoice_id),
  KEY idx_invoice_items_source (source_type, source_id),
  CONSTRAINT fk_items_org          FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_items_invoice_org  FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT chk_invoice_items_type CHECK (item_type IN ('rent','water','electricity','garbage','service_charge','parking','late_fee','deposit','maintenance','adjustment','other'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS payments (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id   BIGINT UNSIGNED NOT NULL,
  lease_id          BIGINT UNSIGNED NOT NULL,
  payment_method_id BIGINT UNSIGNED NOT NULL,
  amount            DECIMAL(12,2) NOT NULL,
  paid_at           DATETIME NOT NULL,
  reference         VARCHAR(100) NULL COMMENT 'M-Pesa receipt, bank ref, cheque no.',
  status            VARCHAR(30) NOT NULL DEFAULT 'completed',
  received_by       BIGINT UNSIGNED NULL,
  notes             VARCHAR(255) NULL,
  created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_payments_org_id (organization_id, id),
  UNIQUE KEY uq_payments_org_method_reference (organization_id, payment_method_id, reference),
  KEY idx_payments_org (organization_id),
  KEY idx_payments_org_lease (organization_id, lease_id),
  KEY idx_payments_lease (lease_id),
  KEY idx_payments_method (payment_method_id),
  KEY idx_payments_receiver (received_by),
  KEY idx_payments_paid_at (paid_at),
  CONSTRAINT fk_payments_org       FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_payments_lease_org FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT fk_payments_method    FOREIGN KEY (payment_method_id) REFERENCES payment_methods (id) ON DELETE RESTRICT,
  CONSTRAINT fk_payments_receiver  FOREIGN KEY (received_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_payments_amount   CHECK (amount > 0),
  CONSTRAINT chk_payments_status   CHECK (status IN ('pending','completed','failed','reversed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS payment_allocations (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  payment_id      BIGINT UNSIGNED NOT NULL,
  invoice_id      BIGINT UNSIGNED NOT NULL,
  amount          DECIMAL(12,2) NOT NULL,
  reversed_at     DATETIME NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_allocation_payment_invoice (payment_id, invoice_id),
  KEY idx_alloc_org_payment (organization_id, payment_id),
  KEY idx_alloc_org_invoice (organization_id, invoice_id),
  KEY idx_allocations_invoice (invoice_id),
  CONSTRAINT fk_alloc_org          FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_alloc_payment_org  FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fk_alloc_invoice_org  FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT chk_alloc_amount CHECK (amount > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS late_fees (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  invoice_id      BIGINT UNSIGNED NOT NULL,
  amount          DECIMAL(12,2) NOT NULL,
  date_assessed   DATE NOT NULL,
  status          VARCHAR(30) NOT NULL DEFAULT 'pending',
  waived_by       BIGINT UNSIGNED NULL,
  waived_reason   VARCHAR(255) NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_late_fees_invoice_date (invoice_id, date_assessed),
  KEY idx_late_fees_org_invoice (organization_id, invoice_id),
  KEY idx_late_fees_waiver (waived_by),
  CONSTRAINT fk_late_fees_org         FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_late_fees_invoice_org FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT fk_late_fees_waiver      FOREIGN KEY (waived_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_late_fees_status CHECK (status IN ('pending','applied','waived','paid'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ---------------------------------------------------------------------------
-- Ledger. ledger_entries is append-only; journal_id groups the debit/credit
-- lines of one balanced posting.
-- ---------------------------------------------------------------------------
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
  ('cash',                  'Cash and bank',                     'asset'),
  ('accounts_receivable',   'Tenant receivables',                'asset'),
  ('unallocated_funds',     'Unallocated tenant funds / credit', 'liability'),
  ('deposit_liability',     'Tenant deposits held',              'liability'),
  ('owner_payable',         'Owner payable',                     'liability'),
  ('tax_payable',           'Tax payable',                       'liability'),
  ('rent_income',           'Rent income',                       'income'),
  ('late_fee_income',       'Late fee income',                   'income'),
  ('utility_income',        'Utility income',                    'income'),
  ('other_income',          'Other income',                      'income'),
  ('management_commission', 'Management commission income',      'income'),
  ('maintenance_expense',   'Maintenance expense',               'expense'),
  ('income_adjustments',    'Credit notes and income adjustments','expense');

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
  KEY idx_ledger_account (account_code),
  KEY idx_ledger_creator (created_by),
  CONSTRAINT fk_ledger_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_ledger_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_ledger_account
    FOREIGN KEY (account_code) REFERENCES ledger_accounts (code) ON DELETE RESTRICT,
  CONSTRAINT fk_ledger_creator
    FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_ledger_sides
    CHECK ((debit_amount > 0 AND credit_amount = 0)
        OR (credit_amount > 0 AND debit_amount = 0))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

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
  KEY idx_credit_notes_creator (created_by),
  CONSTRAINT fk_credit_notes_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_credit_notes_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_credit_notes_invoice
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_credit_notes_creator
    FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_credit_notes_amount CHECK (amount > 0),
  CONSTRAINT chk_credit_notes_status
    CHECK (status IN ('draft','issued','applied','void'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

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
  KEY idx_tenant_credit_creator (created_by),
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
  CONSTRAINT fk_tenant_credit_creator
    FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_tenant_credit_amount CHECK (amount <> 0),
  CONSTRAINT chk_tenant_credit_subject CHECK (lease_id IS NOT NULL OR tenant_id IS NOT NULL),
  CONSTRAINT chk_tenant_credit_type
    CHECK (entry_type IN ('overpayment','credit_note','application','refund','adjustment','reversal'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- A reversal is full-payment, one per payment, and keeps history by marking
-- allocations reversed instead of deleting them.
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
  KEY idx_reversals_org_payment (organization_id, payment_id),
  KEY idx_reversals_user (reversed_by),
  CONSTRAINT fk_reversals_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_reversals_payment
    FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_reversals_user
    FOREIGN KEY (reversed_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_reversals_amount CHECK (amount > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ---------------------------------------------------------------------------
-- Owner money: statement, its lines, commission accruals and disbursements.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS owner_statements (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id  BIGINT UNSIGNED NOT NULL,
  owner_id         BIGINT UNSIGNED NOT NULL,
  property_id      BIGINT UNSIGNED NULL COMMENT 'NULL = consolidated for the owner',
  statement_number VARCHAR(50)     NOT NULL,
  period_start     DATE            NOT NULL,
  period_end       DATE            NOT NULL,
  gross_rent       DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  management_fee   DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  other_deductions DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  net_payable      DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  status           VARCHAR(20)     NOT NULL DEFAULT 'draft',
  issued_at        DATETIME        NULL,
  created_by       BIGINT UNSIGNED NULL,
  created_at       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_owner_statements_org_number (organization_id, statement_number),
  UNIQUE KEY uq_owner_statements_org_id (organization_id, id),
  KEY idx_owner_statements_owner (organization_id, owner_id, period_start),
  KEY idx_owner_statements_property (organization_id, property_id),
  KEY idx_owner_statements_creator (created_by),
  CONSTRAINT fk_owner_statements_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_owner_statements_owner
    FOREIGN KEY (organization_id, owner_id) REFERENCES owners (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_owner_statements_property
    FOREIGN KEY (organization_id, property_id) REFERENCES properties (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_owner_statements_creator
    FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_owner_statements_dates CHECK (period_end >= period_start),
  CONSTRAINT chk_owner_statements_amounts
    CHECK (gross_rent >= 0 AND management_fee >= 0 AND other_deductions >= 0),
  CONSTRAINT chk_owner_statements_status
    CHECK (status IN ('draft','issued','paid','void'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS owner_statement_lines (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  statement_id    BIGINT UNSIGNED NOT NULL,
  line_type       VARCHAR(30)     NOT NULL,
  description     VARCHAR(255)    NOT NULL,
  amount          DECIMAL(12,2)   NOT NULL COMMENT 'positive pays the owner, negative is a deduction',
  source_type     VARCHAR(40)     NULL,
  source_id       BIGINT UNSIGNED NULL,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_owner_statement_lines_statement (organization_id, statement_id),
  CONSTRAINT fk_owner_statement_lines_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_owner_statement_lines_statement
    FOREIGN KEY (organization_id, statement_id) REFERENCES owner_statements (organization_id, id)
    ON DELETE CASCADE,
  CONSTRAINT chk_owner_statement_lines_amount CHECK (amount <> 0),
  CONSTRAINT chk_owner_statement_lines_type
    CHECK (line_type IN ('rent_collected','management_fee','maintenance','deposit_held','commission','adjustment'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS owner_commissions (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  owner_id        BIGINT UNSIGNED NOT NULL,
  lease_id        BIGINT UNSIGNED NULL,
  invoice_id      BIGINT UNSIGNED NULL,
  commission_type VARCHAR(20)     NOT NULL DEFAULT 'percent',
  rate            DECIMAL(5,2)    NULL COMMENT 'percentage when commission_type = percent',
  base_amount     DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  amount          DECIMAL(12,2)   NOT NULL,
  period_start    DATE            NOT NULL,
  period_end      DATE            NOT NULL,
  status          VARCHAR(20)     NOT NULL DEFAULT 'accrued',
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_owner_commissions_owner (organization_id, owner_id, period_start),
  KEY idx_owner_commissions_lease (organization_id, lease_id),
  KEY idx_owner_commissions_invoice (organization_id, invoice_id),
  CONSTRAINT fk_owner_commissions_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_owner_commissions_owner
    FOREIGN KEY (organization_id, owner_id) REFERENCES owners (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_owner_commissions_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_owner_commissions_invoice
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT chk_owner_commissions_dates CHECK (period_end >= period_start),
  CONSTRAINT chk_owner_commissions_amounts
    CHECK (base_amount >= 0 AND amount >= 0 AND (rate IS NULL OR (rate >= 0 AND rate <= 100))),
  CONSTRAINT chk_owner_commissions_type
    CHECK (commission_type IN ('percent','fixed')),
  CONSTRAINT chk_owner_commissions_status
    CHECK (status IN ('accrued','statemented','paid','void'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS owner_disbursements (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  owner_id        BIGINT UNSIGNED NOT NULL,
  statement_id    BIGINT UNSIGNED NULL,
  amount          DECIMAL(12,2)   NOT NULL,
  method          VARCHAR(30)     NOT NULL,
  reference       VARCHAR(100)    NULL,
  status          VARCHAR(20)     NOT NULL DEFAULT 'pending',
  paid_at         DATETIME        NULL,
  created_by      BIGINT UNSIGNED NULL,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_owner_disbursements_reference (organization_id, reference),
  KEY idx_owner_disbursements_owner (organization_id, owner_id, paid_at),
  KEY idx_owner_disbursements_statement (organization_id, statement_id),
  KEY idx_owner_disbursements_creator (created_by),
  CONSTRAINT fk_owner_disbursements_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_owner_disbursements_owner
    FOREIGN KEY (organization_id, owner_id) REFERENCES owners (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_owner_disbursements_statement
    FOREIGN KEY (organization_id, statement_id) REFERENCES owner_statements (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_owner_disbursements_creator
    FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_owner_disbursements_amount CHECK (amount > 0),
  CONSTRAINT chk_owner_disbursements_method
    CHECK (method IN ('mpesa','bank','cheque','cash')),
  CONSTRAINT chk_owner_disbursements_status
    CHECK (status IN ('pending','paid','failed','reversed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ---------------------------------------------------------------------------
-- Append-only guards.
-- ---------------------------------------------------------------------------
CREATE TRIGGER trg_ledger_entries_no_update BEFORE UPDATE ON ledger_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'ledger_entries is append-only';

CREATE TRIGGER trg_ledger_entries_no_delete BEFORE DELETE ON ledger_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'ledger_entries is append-only';

CREATE TRIGGER trg_tenant_credit_no_update BEFORE UPDATE ON tenant_credit_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'tenant_credit_entries is append-only';

CREATE TRIGGER trg_tenant_credit_no_delete BEFORE DELETE ON tenant_credit_entries
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'tenant_credit_entries is append-only';

-- An issued credit note is immutable; it can only move to applied/void.
CREATE TRIGGER trg_credit_notes_issued_immutable BEFORE UPDATE ON credit_notes
FOR EACH ROW
BEGIN
  IF OLD.status <> 'draft' THEN
    IF NEW.status = 'draft' THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'An issued credit note cannot return to draft';
    END IF;
    IF NEW.organization_id <> OLD.organization_id
       OR NEW.lease_id <> OLD.lease_id
       OR (NEW.invoice_id IS NULL) <> (OLD.invoice_id IS NULL)
       OR (NEW.invoice_id IS NOT NULL AND NEW.invoice_id <> OLD.invoice_id)
       OR NEW.amount <> OLD.amount
       OR NEW.issue_date <> OLD.issue_date THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'An issued credit note cannot be changed';
    END IF;
  END IF;
  IF OLD.status IN ('applied','void') AND NEW.status <> OLD.status THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'A finalised credit note cannot change status';
  END IF;
END;

-- ---------------------------------------------------------------------------
-- Invoice amount_paid is derived from live allocations.
-- ---------------------------------------------------------------------------
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
  IF NEW.amount_paid > NEW.total THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'invoices.amount_paid cannot exceed the invoice total';
  END IF;
END;

-- ---------------------------------------------------------------------------
-- Allocations.
-- ---------------------------------------------------------------------------
CREATE TRIGGER trg_allocations_validate BEFORE INSERT ON payment_allocations
FOR EACH ROW
BEGIN
  DECLARE pay_amount    DECIMAL(12,2);
  DECLARE pay_lease     BIGINT UNSIGNED;
  DECLARE pay_status    VARCHAR(30);
  DECLARE invoice_lease BIGINT UNSIGNED;
  DECLARE invoice_total DECIMAL(12,2);
  DECLARE invoice_paid  DECIMAL(12,2);
  DECLARE invoice_status VARCHAR(30);
  DECLARE allocated     DECIMAL(12,2);

  SELECT amount, lease_id, status INTO pay_amount, pay_lease, pay_status
  FROM payments WHERE id = NEW.payment_id;
  IF pay_amount IS NULL THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Payment does not exist';
  END IF;
  IF pay_status <> 'completed' THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Allocations require a completed payment';
  END IF;

  SELECT lease_id, total, amount_paid, status
    INTO invoice_lease, invoice_total, invoice_paid, invoice_status
  FROM invoices WHERE id = NEW.invoice_id;
  IF invoice_lease IS NULL OR invoice_lease <> pay_lease THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Payment and invoice must belong to the same lease';
  END IF;
  IF invoice_status IN ('draft','void') THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Cannot allocate to a draft or void invoice';
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

-- ---------------------------------------------------------------------------
-- Payments and reversals.
-- ---------------------------------------------------------------------------
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
