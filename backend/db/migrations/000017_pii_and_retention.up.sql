-- PII at rest and retention.
--
-- Plaintext columns for national ID, KRA PIN, income and payout details are
-- dropped. Replacements store AES-256-GCM ciphertext produced by
-- internal/platform/pii; national_id_hash is a keyed HMAC-SHA256 of the
-- normalised value so duplicates can still be detected without decrypting.
--
-- This migration intentionally discards existing plaintext values. Back up the
-- database first if they must be re-entered through the application.

ALTER TABLE users
  DROP INDEX idx_users_national_id,
  DROP COLUMN national_id,
  ADD COLUMN national_id_enc VARBINARY(1024) NULL AFTER last_name,
  ADD COLUMN national_id_hash BINARY(32) NULL AFTER national_id_enc,
  ADD COLUMN national_id_last4 CHAR(4) NULL AFTER national_id_hash,
  ADD COLUMN pii_retention_until DATE NULL,
  ADD COLUMN pii_anonymized_at DATETIME NULL,
  ADD KEY idx_users_national_id_hash (national_id_hash);

ALTER TABLE tenants
  DROP COLUMN kra_pin,
  DROP COLUMN income,
  ADD COLUMN kra_pin_enc VARBINARY(1024) NULL AFTER emergency_phone,
  ADD COLUMN income_enc VARBINARY(1024) NULL AFTER employer_name,
  ADD COLUMN pii_retention_until DATE NULL,
  ADD COLUMN pii_anonymized_at DATETIME NULL;

ALTER TABLE owners
  DROP COLUMN kra_pin,
  DROP COLUMN payout_mpesa_number,
  DROP COLUMN payout_bank_name,
  DROP COLUMN payout_account_name,
  ADD COLUMN kra_pin_enc VARBINARY(1024) NULL AFTER company,
  ADD COLUMN payout_mpesa_number_enc VARBINARY(1024) NULL AFTER payout_method,
  ADD COLUMN payout_bank_name_enc VARBINARY(1024) NULL AFTER payout_mpesa_number_enc,
  ADD COLUMN payout_account_name_enc VARBINARY(1024) NULL AFTER payout_bank_name_enc,
  ADD COLUMN pii_retention_until DATE NULL,
  ADD COLUMN pii_anonymized_at DATETIME NULL;

ALTER TABLE contractors
  DROP COLUMN kra_pin,
  ADD COLUMN kra_pin_enc VARBINARY(1024) NULL AFTER specialty,
  ADD COLUMN pii_retention_until DATE NULL,
  ADD COLUMN pii_anonymized_at DATETIME NULL;

CREATE TABLE IF NOT EXISTS pii_retention_policies (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NULL COMMENT 'NULL = platform default',
  org_scope       BIGINT UNSIGNED GENERATED ALWAYS AS (IFNULL(organization_id, 0)) STORED,
  entity_type     VARCHAR(30)     NOT NULL,
  retention_days  INT UNSIGNED    NOT NULL,
  action          VARCHAR(20)     NOT NULL DEFAULT 'anonymize',
  description     VARCHAR(255)    NULL,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pii_retention_scope_entity (org_scope, entity_type),
  CONSTRAINT fk_pii_retention_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
  CONSTRAINT chk_pii_retention_days CHECK (retention_days > 0),
  CONSTRAINT chk_pii_retention_action CHECK (action IN ('anonymize','delete')),
  CONSTRAINT chk_pii_retention_entity
    CHECK (entity_type IN ('users','tenants','owners','contractors','messages','notifications'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO pii_retention_policies
  (organization_id, entity_type, retention_days, action, description)
VALUES
  (NULL, 'users',         2555, 'anonymize', 'Seven years after the relationship ends'),
  (NULL, 'tenants',       2555, 'anonymize', 'Seven years for tenancy and financial records'),
  (NULL, 'owners',        2555, 'anonymize', 'Seven years for owner payout records'),
  (NULL, 'contractors',   1825, 'anonymize', 'Five years after the last job'),
  (NULL, 'messages',       730, 'delete',    'Two years of in-app messages'),
  (NULL, 'notifications',  365, 'delete',    'One year of delivery records');

-- Append-only access trail for sensitive fields, separate from audit_logs so
-- it can be retained/redacted under its own policy. No FKs: the trail must
-- survive user/organization deletion.
CREATE TABLE IF NOT EXISTS pii_access_logs (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NULL,
  user_id         BIGINT UNSIGNED NULL,
  subject_type    VARCHAR(30)     NOT NULL,
  subject_id      BIGINT UNSIGNED NOT NULL,
  fields          JSON            NULL,
  reason          VARCHAR(100)    NULL,
  ip_address      VARCHAR(45)     NULL,
  user_agent      VARCHAR(255)    NULL,
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_pii_access_org_date (organization_id, created_at),
  KEY idx_pii_access_subject (subject_type, subject_id),
  KEY idx_pii_access_user (user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TRIGGER trg_pii_access_no_update BEFORE UPDATE ON pii_access_logs
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'pii_access_logs is append-only';

CREATE TRIGGER trg_pii_access_no_delete BEFORE DELETE ON pii_access_logs
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'pii_access_logs is append-only';
