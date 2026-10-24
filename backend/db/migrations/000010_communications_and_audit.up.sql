-- Notifications (email/SMS/WhatsApp), in-app messages, reviews, the audit trail,
-- PII retention policies and the append-only access log for sensitive fields.
CREATE TABLE IF NOT EXISTS notifications (
  id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id     BIGINT UNSIGNED NOT NULL,
  user_id             BIGINT UNSIGNED NOT NULL,
  channel             VARCHAR(20)  NOT NULL,
  type                VARCHAR(50)  NOT NULL,
  title               VARCHAR(150) NULL,
  body                TEXT NOT NULL,
  payload             JSON NULL,
  status              VARCHAR(20)  NOT NULL DEFAULT 'queued',
  scheduled_at        DATETIME NULL,
  sent_at             DATETIME NULL,
  read_at             DATETIME NULL,
  provider_message_id VARCHAR(100) NULL,
  error_message       VARCHAR(255) NULL,
  created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_notifications_user (user_id),
  KEY idx_notifications_org (organization_id),
  KEY idx_notifications_queue (status, scheduled_at),
  CONSTRAINT fk_notifications_org  FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_notifications_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT chk_notifications_channel CHECK (channel IN ('email','sms','whatsapp','push','in_app')),
  CONSTRAINT chk_notifications_status  CHECK (status IN ('queued','sent','delivered','failed','read'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- In-app messages belong to one organization. A platform admin (organization_id
-- NULL) may take part in any conversation; the triggers below enforce that the
-- other participant belongs to the message's organization.
CREATE TABLE IF NOT EXISTS messages (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  sender_id       BIGINT UNSIGNED NOT NULL,
  receiver_id     BIGINT UNSIGNED NOT NULL,
  subject         VARCHAR(150) NULL,
  body            TEXT NOT NULL,
  date_sent       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  is_read         TINYINT(1) NOT NULL DEFAULT 0,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_messages_sender (sender_id),
  KEY idx_messages_receiver (receiver_id, is_read),
  KEY idx_messages_org_date (organization_id, date_sent),
  CONSTRAINT fk_messages_org      FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_messages_sender   FOREIGN KEY (sender_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT fk_messages_receiver FOREIGN KEY (receiver_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TRIGGER trg_messages_org_ins BEFORE INSERT ON messages
FOR EACH ROW
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.sender_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message sender does not belong to the organization';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.receiver_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message receiver does not belong to the organization';
  END IF;
END;

CREATE TRIGGER trg_messages_org_upd BEFORE UPDATE ON messages
FOR EACH ROW
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.sender_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message sender does not belong to the organization';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM users
    WHERE id = NEW.receiver_id
      AND (organization_id = NEW.organization_id OR organization_id IS NULL)
  ) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Message receiver does not belong to the organization';
  END IF;
END;

CREATE TABLE IF NOT EXISTS reviews (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NOT NULL,
  tenant_id       BIGINT UNSIGNED NOT NULL,
  property_id     BIGINT UNSIGNED NOT NULL,
  rating          TINYINT UNSIGNED NOT NULL,
  content         TEXT NULL,
  date_submitted  DATE NOT NULL DEFAULT (CURRENT_DATE),
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_reviews_tenant_property (tenant_id, property_id),
  KEY idx_reviews_org_property (organization_id, property_id),
  KEY idx_reviews_org_tenant (organization_id, tenant_id),
  CONSTRAINT fk_reviews_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_reviews_tenant_org
    FOREIGN KEY (organization_id, tenant_id) REFERENCES tenants (organization_id, id)
    ON DELETE CASCADE,
  CONSTRAINT fk_reviews_property_org
    FOREIGN KEY (organization_id, property_id) REFERENCES properties (organization_id, id)
    ON DELETE CASCADE,
  CONSTRAINT chk_reviews_rating CHECK (rating BETWEEN 1 AND 5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Append-only: grant the app user INSERT and SELECT only on this table.
-- No FKs on purpose, so audit rows survive deletion of users/organizations.
CREATE TABLE IF NOT EXISTS audit_logs (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id BIGINT UNSIGNED NULL,
  user_id         BIGINT UNSIGNED NULL,
  action          VARCHAR(50)  NOT NULL,
  entity_type     VARCHAR(100) NOT NULL,
  entity_id       BIGINT UNSIGNED NULL,
  old_values      JSON NULL,
  new_values      JSON NULL,
  ip_address      VARCHAR(45)  NULL,
  user_agent      VARCHAR(255) NULL,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_audit_org (organization_id, created_at),
  KEY idx_audit_entity (entity_type, entity_id),
  KEY idx_audit_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TRIGGER trg_audit_logs_no_update BEFORE UPDATE ON audit_logs
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'audit_logs is append-only';

CREATE TRIGGER trg_audit_logs_no_delete BEFORE DELETE ON audit_logs
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'audit_logs is append-only';

-- Retention: rows with a retention date can be anonymised or deleted by entity.
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
  KEY idx_pii_retention_org (organization_id),
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

-- Append-only access trail for sensitive fields, separate from audit_logs so it
-- can be retained/redacted under its own policy. No FKs: the trail must survive
-- user/organization deletion.
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
