-- Invoices, line items, payments and their allocation to invoices, late fees.
CREATE TABLE IF NOT EXISTS payment_methods (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  code        VARCHAR(30)  NOT NULL,
  name        VARCHAR(50)  NOT NULL,
  description VARCHAR(255) NULL,
  is_active   TINYINT(1) NOT NULL DEFAULT 1,
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
  created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_invoices_org_number (organization_id, invoice_number),
  UNIQUE KEY uq_invoices_lease_period (lease_id, invoice_type, billing_period_start),
  KEY idx_invoices_due (due_date),
  KEY idx_invoices_status (status),
  CONSTRAINT fk_invoices_org     FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_invoices_lease   FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE RESTRICT,
  CONSTRAINT fk_invoices_creator FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_invoices_amounts CHECK (total >= 0 AND amount_paid >= 0),
  CONSTRAINT chk_invoices_status CHECK (status IN ('draft','issued','partially_paid','paid','overdue','void'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS invoice_items (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  invoice_id  BIGINT UNSIGNED NOT NULL,
  item_type   VARCHAR(30)  NOT NULL DEFAULT 'rent',
  description VARCHAR(255) NOT NULL,
  quantity    DECIMAL(12,3) NOT NULL DEFAULT 1.000,
  unit_price  DECIMAL(12,2) NOT NULL,
  tax_rate    DECIMAL(5,2)  NOT NULL DEFAULT 0.00,
  amount      DECIMAL(12,2) NOT NULL,
  source_type VARCHAR(50) NULL COMMENT 'e.g. utility_bill, late_fee, maintenance_expense',
  source_id   BIGINT UNSIGNED NULL,
  PRIMARY KEY (id),
  KEY idx_invoice_items_invoice (invoice_id),
  KEY idx_invoice_items_source (source_type, source_id),
  CONSTRAINT fk_invoice_items_invoice FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE CASCADE,
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
  created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_payments_method_reference (payment_method_id, reference),
  KEY idx_payments_org (organization_id),
  KEY idx_payments_lease (lease_id),
  KEY idx_payments_paid_at (paid_at),
  CONSTRAINT fk_payments_org      FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_payments_lease    FOREIGN KEY (lease_id) REFERENCES leases (id) ON DELETE RESTRICT,
  CONSTRAINT fk_payments_method   FOREIGN KEY (payment_method_id) REFERENCES payment_methods (id) ON DELETE RESTRICT,
  CONSTRAINT fk_payments_receiver FOREIGN KEY (received_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_payments_amount  CHECK (amount > 0),
  CONSTRAINT chk_payments_status  CHECK (status IN ('pending','completed','failed','reversed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS payment_allocations (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  payment_id BIGINT UNSIGNED NOT NULL,
  invoice_id BIGINT UNSIGNED NOT NULL,
  amount     DECIMAL(12,2) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_allocation_payment_invoice (payment_id, invoice_id),
  KEY idx_allocations_invoice (invoice_id),
  CONSTRAINT fk_alloc_payment FOREIGN KEY (payment_id) REFERENCES payments (id) ON DELETE CASCADE,
  CONSTRAINT fk_alloc_invoice FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE RESTRICT,
  CONSTRAINT chk_alloc_amount CHECK (amount > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS late_fees (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  invoice_id     BIGINT UNSIGNED NOT NULL,
  amount         DECIMAL(12,2) NOT NULL,
  date_assessed  DATE NOT NULL,
  status         VARCHAR(30) NOT NULL DEFAULT 'pending',
  waived_by      BIGINT UNSIGNED NULL,
  waived_reason  VARCHAR(255) NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_late_fees_invoice (invoice_id),
  CONSTRAINT fk_late_fees_invoice FOREIGN KEY (invoice_id) REFERENCES invoices (id) ON DELETE RESTRICT,
  CONSTRAINT fk_late_fees_waiver  FOREIGN KEY (waived_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_late_fees_status CHECK (status IN ('pending','applied','waived','paid'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
