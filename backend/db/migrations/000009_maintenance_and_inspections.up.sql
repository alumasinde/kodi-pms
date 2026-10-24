-- Contractors, maintenance requests/jobs/expenses/logs, and unit inspections.
CREATE TABLE IF NOT EXISTS contractors (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  user_id         BIGINT UNSIGNED NULL,
  company         VARCHAR(150) NOT NULL,
  contact         VARCHAR(150) NULL,
  phone           VARCHAR(20)  NULL,
  email           VARCHAR(255) NULL,
  specialty       VARCHAR(100) NULL,
  kra_pin         VARCHAR(20)  NULL,
  status          VARCHAR(30)  NOT NULL DEFAULT 'active',
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_contractors_org (organization_id),
  CONSTRAINT fk_contractors_org  FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_contractors_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS maintenance_requests (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  unit_id         BIGINT UNSIGNED NOT NULL,
  tenant_id       BIGINT UNSIGNED NULL,
  assigned_to     BIGINT UNSIGNED NULL,
  title           VARCHAR(150) NOT NULL,
  description     TEXT NULL,
  priority        VARCHAR(30) NOT NULL DEFAULT 'medium',
  status          VARCHAR(30) NOT NULL DEFAULT 'open',
  date_requested  DATE NOT NULL,
  resolved_at     TIMESTAMP NULL,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_mreq_org (organization_id),
  KEY idx_mreq_unit (unit_id),
  KEY idx_mreq_tenant (tenant_id),
  KEY idx_mreq_status (status),
  CONSTRAINT fk_mreq_org      FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_mreq_unit     FOREIGN KEY (unit_id) REFERENCES units (id) ON DELETE RESTRICT,
  CONSTRAINT fk_mreq_tenant   FOREIGN KEY (tenant_id) REFERENCES tenants (id) ON DELETE SET NULL,
  CONSTRAINT fk_mreq_assignee FOREIGN KEY (assigned_to) REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT chk_mreq_priority CHECK (priority IN ('low','medium','high','urgent')),
  CONSTRAINT chk_mreq_status   CHECK (status IN ('open','assigned','in_progress','on_hold','resolved','closed','cancelled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS maintenance_jobs (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id     BIGINT UNSIGNED NOT NULL,
  contractor_id  BIGINT UNSIGNED NULL,
  date_scheduled DATE NULL,
  date_completed DATE NULL,
  status         VARCHAR(30) NOT NULL DEFAULT 'scheduled',
  notes          TEXT NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_mjobs_request (request_id),
  KEY idx_mjobs_contractor (contractor_id),
  CONSTRAINT fk_mjobs_request    FOREIGN KEY (request_id) REFERENCES maintenance_requests (id) ON DELETE CASCADE,
  CONSTRAINT fk_mjobs_contractor FOREIGN KEY (contractor_id) REFERENCES contractors (id) ON DELETE SET NULL,
  CONSTRAINT chk_mjobs_status    CHECK (status IN ('scheduled','in_progress','completed','cancelled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS maintenance_expenses (
  id                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  job_id             BIGINT UNSIGNED NOT NULL,
  amount             DECIMAL(12,2) NOT NULL,
  date_incurred      DATE NOT NULL,
  description        VARCHAR(255) NULL,
  invoice_number     VARCHAR(100) NULL,
  charge_to_tenant   TINYINT(1) NOT NULL DEFAULT 0,
  created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_mexp_job (job_id),
  CONSTRAINT fk_mexp_job FOREIGN KEY (job_id) REFERENCES maintenance_jobs (id) ON DELETE RESTRICT,
  CONSTRAINT chk_mexp_amount CHECK (amount >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS maintenance_logs (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id  BIGINT UNSIGNED NOT NULL,
  user_id     BIGINT UNSIGNED NULL,
  event_type  VARCHAR(30) NOT NULL DEFAULT 'comment',
  from_status VARCHAR(30) NULL,
  to_status   VARCHAR(30) NULL,
  message     TEXT NULL,
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_mlogs_request (request_id),
  CONSTRAINT fk_mlogs_request FOREIGN KEY (request_id) REFERENCES maintenance_requests (id) ON DELETE CASCADE,
  CONSTRAINT fk_mlogs_user    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS inspections (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  unit_id        BIGINT UNSIGNED NOT NULL,
  inspector_id   BIGINT UNSIGNED NOT NULL,
  date_conducted DATE NOT NULL,
  type           VARCHAR(50) NOT NULL DEFAULT 'routine',
  status         VARCHAR(30) NOT NULL DEFAULT 'scheduled',
  notes          TEXT NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_inspections_unit (unit_id),
  KEY idx_inspections_inspector (inspector_id),
  CONSTRAINT fk_inspections_unit      FOREIGN KEY (unit_id) REFERENCES units (id) ON DELETE RESTRICT,
  CONSTRAINT fk_inspections_inspector FOREIGN KEY (inspector_id) REFERENCES users (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS inspection_items (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  inspection_id    BIGINT UNSIGNED NOT NULL,
  area             VARCHAR(100) NOT NULL,
  condition_rating VARCHAR(100) NULL COMMENT 'was "condition" - a reserved word in MySQL',
  is_acceptable    TINYINT(1) NOT NULL DEFAULT 1,
  comments         TEXT NULL,
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_inspection_items_inspection (inspection_id),
  CONSTRAINT fk_inspection_items_inspection FOREIGN KEY (inspection_id) REFERENCES inspections (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
