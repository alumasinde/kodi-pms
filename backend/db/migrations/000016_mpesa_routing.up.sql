-- M-Pesa routing: a webhook identifies the organization by shortcode and the
-- lease by the account reference (BillRefNumber), instead of guessing.
--
-- Credential columns hold ciphertext produced by the PII cipher (AES-256-GCM),
-- never plaintext. callback_token_hash is SHA-256 of the per-shortcode callback
-- token; the raw token is only shown when it is created.

CREATE TABLE IF NOT EXISTS mpesa_shortcodes (
  id                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id          BIGINT UNSIGNED NOT NULL,
  shortcode                VARCHAR(20)     NOT NULL COMMENT 'Paybill or Till number',
  account_type             VARCHAR(20)     NOT NULL DEFAULT 'paybill',
  environment              VARCHAR(20)     NOT NULL DEFAULT 'production',
  consumer_key_enc         VARBINARY(2048) NULL,
  consumer_secret_enc      VARBINARY(2048) NULL,
  passkey_enc              VARBINARY(2048) NULL,
  initiator_name           VARCHAR(100)    NULL,
  security_credential_enc  VARBINARY(4096) NULL,
  callback_token_hash      CHAR(64)        NULL,
  is_active                TINYINT(1)      NOT NULL DEFAULT 1,
  created_at               DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at               DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_mpesa_shortcodes_env_code (environment, shortcode),
  UNIQUE KEY uq_mpesa_shortcodes_org_id (organization_id, id),
  KEY idx_mpesa_shortcodes_org (organization_id),
  CONSTRAINT fk_mpesa_shortcodes_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT chk_mpesa_shortcodes_type CHECK (account_type IN ('paybill','till')),
  CONSTRAINT chk_mpesa_shortcodes_env CHECK (environment IN ('sandbox','production'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS mpesa_account_refs (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id  BIGINT UNSIGNED NOT NULL,
  shortcode_id     BIGINT UNSIGNED NOT NULL,
  lease_id         BIGINT UNSIGNED NOT NULL,
  account_reference VARCHAR(100)   NOT NULL COMMENT 'Value the tenant enters, e.g. KD-001-A1',
  is_active        TINYINT(1)      NOT NULL DEFAULT 1,
  created_at       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_mpesa_refs_shortcode_ref (shortcode_id, account_reference),
  KEY idx_mpesa_refs_org_lease (organization_id, lease_id),
  CONSTRAINT fk_mpesa_refs_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_mpesa_refs_shortcode
    FOREIGN KEY (organization_id, shortcode_id) REFERENCES mpesa_shortcodes (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_mpesa_refs_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE mpesa_transactions
  ADD COLUMN shortcode_id BIGINT UNSIGNED NULL AFTER business_short_code,
  ADD COLUMN lease_id     BIGINT UNSIGNED NULL AFTER bill_ref_number,
  ADD KEY idx_mpesa_shortcode (organization_id, shortcode_id),
  ADD KEY idx_mpesa_org_lease (organization_id, lease_id),
  ADD CONSTRAINT fk_mpesa_shortcode
    FOREIGN KEY (organization_id, shortcode_id) REFERENCES mpesa_shortcodes (organization_id, id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT fk_mpesa_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT;

ALTER TABLE mpesa_stk_requests
  ADD COLUMN shortcode_id BIGINT UNSIGNED NULL AFTER organization_id,
  ADD KEY idx_stk_shortcode (organization_id, shortcode_id),
  ADD CONSTRAINT fk_stk_shortcode
    FOREIGN KEY (organization_id, shortcode_id) REFERENCES mpesa_shortcodes (organization_id, id)
    ON DELETE RESTRICT;
