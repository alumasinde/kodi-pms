-- Notifications (email/SMS/WhatsApp), in-app messages, reviews, and the audit trail.
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
  scheduled_at        TIMESTAMP NULL,
  sent_at             TIMESTAMP NULL,
  read_at             TIMESTAMP NULL,
  provider_message_id VARCHAR(100) NULL,
  error_message       VARCHAR(255) NULL,
  created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_notifications_user (user_id),
  KEY idx_notifications_org (organization_id),
  KEY idx_notifications_queue (status, scheduled_at),
  CONSTRAINT fk_notifications_org  FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_notifications_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT chk_notifications_channel CHECK (channel IN ('email','sms','whatsapp','push','in_app')),
  CONSTRAINT chk_notifications_status  CHECK (status IN ('queued','sent','delivered','failed','read'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS messages (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  sender_id   BIGINT UNSIGNED NOT NULL,
  receiver_id BIGINT UNSIGNED NOT NULL,
  subject     VARCHAR(150) NULL,
  body        TEXT NOT NULL,
  date_sent   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  is_read     TINYINT(1) NOT NULL DEFAULT 0,
  PRIMARY KEY (id),
  KEY idx_messages_sender (sender_id),
  KEY idx_messages_receiver (receiver_id, is_read),
  CONSTRAINT fk_messages_sender   FOREIGN KEY (sender_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT fk_messages_receiver FOREIGN KEY (receiver_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS reviews (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id      BIGINT UNSIGNED NOT NULL,
  property_id    BIGINT UNSIGNED NOT NULL,
  rating         TINYINT UNSIGNED NOT NULL,
  content        TEXT NULL,
  date_submitted DATE NOT NULL DEFAULT (CURRENT_DATE),
  PRIMARY KEY (id),
  UNIQUE KEY uq_reviews_tenant_property (tenant_id, property_id),
  KEY idx_reviews_property (property_id),
  CONSTRAINT fk_reviews_tenant   FOREIGN KEY (tenant_id) REFERENCES tenants (id) ON DELETE CASCADE,
  CONSTRAINT fk_reviews_property FOREIGN KEY (property_id) REFERENCES properties (id) ON DELETE CASCADE,
  CONSTRAINT chk_reviews_rating  CHECK (rating BETWEEN 1 AND 5)
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
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_audit_org (organization_id, created_at),
  KEY idx_audit_entity (entity_type, entity_id),
  KEY idx_audit_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
