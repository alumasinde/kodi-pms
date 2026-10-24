-- Leases, who is on them, deposits, documents and tenant insurance.
--
-- Every lease child carries organization_id so the composite foreign keys make
-- cross-organization references impossible. One active lease per unit is
-- enforced with a generated column and a unique key: moving a lease out of
-- 'active' releases the unit for the next tenancy.
CREATE TABLE IF NOT EXISTS leases (
  id                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id    BIGINT UNSIGNED NOT NULL,
  unit_id            BIGINT UNSIGNED NOT NULL,
  lease_number       VARCHAR(50) NOT NULL,
  start_date         DATE NOT NULL,
  end_date           DATE NULL COMMENT 'NULL = periodic / open-ended tenancy',
  rent_amount        DECIMAL(12,2) NOT NULL,
  deposit_amount     DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  billing_day        TINYINT UNSIGNED NOT NULL DEFAULT 1,
  due_day            TINYINT UNSIGNED NOT NULL DEFAULT 5,
  notice_period_days INT UNSIGNED NOT NULL DEFAULT 30,
  status             VARCHAR(30) NOT NULL DEFAULT 'draft',
  signed_at          DATETIME NULL,
  terminated_at      DATETIME NULL,
  termination_reason VARCHAR(255) NULL,
  created_by         BIGINT UNSIGNED NULL,
  active_unit_id     BIGINT UNSIGNED GENERATED ALWAYS AS (IF(status = 'active', unit_id, NULL)) STORED,
  created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_leases_org_number (organization_id, lease_number),
  UNIQUE KEY uq_leases_org_id (organization_id, id),
  UNIQUE KEY uq_leases_active_unit (active_unit_id),
  KEY idx_leases_unit (unit_id),
  KEY idx_leases_org_unit (organization_id, unit_id),
  KEY idx_leases_creator (created_by),
  KEY idx_leases_status (status),
  KEY idx_leases_end_date (end_date),
  CONSTRAINT fk_leases_org      FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_leases_unit_org FOREIGN KEY (organization_id, unit_id) REFERENCES units (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT fk_leases_creator  FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_leases_dates   CHECK (end_date IS NULL OR end_date >= start_date),
  CONSTRAINT chk_leases_amounts CHECK (rent_amount >= 0 AND deposit_amount >= 0),
  CONSTRAINT chk_leases_status  CHECK (status IN ('draft','pending_signature','active','expired','terminated','renewed')),
  CONSTRAINT chk_leases_billing_day CHECK (billing_day BETWEEN 1 AND 28),
  CONSTRAINT chk_leases_due_day     CHECK (due_day BETWEEN 1 AND 28)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS lease_tenants (
  organization_id BIGINT UNSIGNED NOT NULL,
  lease_id        BIGINT UNSIGNED NOT NULL,
  tenant_id       BIGINT UNSIGNED NOT NULL,
  role            VARCHAR(20) NOT NULL DEFAULT 'primary',
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (lease_id, tenant_id),
  KEY idx_lease_tenants_tenant (tenant_id),
  KEY idx_lt_org_lease (organization_id, lease_id),
  KEY idx_lt_org_tenant (organization_id, tenant_id),
  CONSTRAINT fk_lt_org        FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_lt_lease_org  FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fk_lt_tenant_org FOREIGN KEY (organization_id, tenant_id) REFERENCES tenants (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT chk_lt_role      CHECK (role IN ('primary','co_tenant','guarantor','occupant'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS deposits (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  lease_id        BIGINT UNSIGNED NOT NULL,
  amount          DECIMAL(12,2) NOT NULL,
  date_paid       DATE NULL,
  status          VARCHAR(30) NOT NULL DEFAULT 'pending',
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_deposits_org_id (organization_id, id),
  KEY idx_deposits_lease (lease_id),
  KEY idx_deposits_org_lease (organization_id, lease_id),
  CONSTRAINT fk_deposits_org       FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_deposits_lease_org FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT chk_deposits_amount CHECK (amount >= 0),
  CONSTRAINT chk_deposits_status CHECK (status IN ('pending','held','partially_refunded','refunded','forfeited'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS deposit_refunds (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  deposit_id      BIGINT UNSIGNED NOT NULL,
  amount          DECIMAL(12,2) NOT NULL,
  deductions      DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  date_refunded   DATE NOT NULL,
  reason          TEXT NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_deposit_refunds_deposit (deposit_id),
  KEY idx_refunds_org_deposit (organization_id, deposit_id),
  CONSTRAINT fk_refunds_org          FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_refunds_deposit_org  FOREIGN KEY (organization_id, deposit_id) REFERENCES deposits (organization_id, id) ON DELETE RESTRICT,
  CONSTRAINT chk_refunds_amounts CHECK (amount >= 0 AND deductions >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS documents (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  lease_id        BIGINT UNSIGNED NOT NULL,
  name            VARCHAR(150) NOT NULL,
  type            VARCHAR(50)  NOT NULL DEFAULT 'other',
  file_path       VARCHAR(255) NOT NULL,
  uploaded_by     BIGINT UNSIGNED NULL,
  date_uploaded   DATE NOT NULL DEFAULT (CURRENT_DATE),
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_documents_lease (lease_id),
  KEY idx_documents_org_lease (organization_id, lease_id),
  KEY idx_documents_uploader (uploaded_by),
  CONSTRAINT fk_documents_org        FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_documents_lease_org  FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fk_documents_uploader   FOREIGN KEY (uploaded_by) REFERENCES users (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS insurance_policies (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  lease_id        BIGINT UNSIGNED NOT NULL,
  provider        VARCHAR(150) NOT NULL,
  number          VARCHAR(100) NOT NULL,
  coverage        DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  start_date      DATE NOT NULL,
  end_date        DATE NOT NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_insurance_lease (lease_id),
  KEY idx_insurance_org_lease (organization_id, lease_id),
  CONSTRAINT fk_insurance_org        FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_insurance_lease_org  FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
