-- Owner money: monthly statements, commission accruals and disbursements.
-- Owners already carry a management fee and payout details; these tables make
-- the actual payable, its lines and payments auditable.

CREATE TABLE IF NOT EXISTS owner_statements (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  owner_id        BIGINT UNSIGNED NOT NULL,
  property_id     BIGINT UNSIGNED NULL COMMENT 'NULL = consolidated for the owner',
  statement_number VARCHAR(50)    NOT NULL,
  period_start    DATE            NOT NULL,
  period_end      DATE            NOT NULL,
  gross_rent      DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  management_fee  DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  other_deductions DECIMAL(12,2)  NOT NULL DEFAULT 0.00,
  net_payable     DECIMAL(12,2)   NOT NULL DEFAULT 0.00,
  status          VARCHAR(20)     NOT NULL DEFAULT 'draft',
  issued_at       DATETIME        NULL,
  created_by      BIGINT UNSIGNED NULL,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_owner_statements_org_number (organization_id, statement_number),
  UNIQUE KEY uq_owner_statements_org_id (organization_id, id),
  KEY idx_owner_statements_owner (organization_id, owner_id, period_start),
  KEY idx_owner_statements_property (organization_id, property_id),
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
    CHECK (line_type IN ('rent_collected','management_fee','maintenance','commission','deposit_held','adjustment','disbursement'))
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
