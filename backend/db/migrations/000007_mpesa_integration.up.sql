-- M-Pesa (Daraja) routing, C2B confirmations and STK push requests.
--
-- A webhook identifies the organization by shortcode and the lease by the
-- account reference (BillRefNumber), instead of guessing. Credential columns
-- hold AES-256-GCM ciphertext produced by internal/platform/pii, never
-- plaintext. callback_token_hash holds the lowercase hex of that package's
-- keyed HMAC-SHA256 of the per-shortcode callback token (64 characters, and
-- the package exposes no plain SHA-256); it is what a callback is matched
-- against, indexed, and unreadable back into a token. callback_token_enc holds
-- the same token sealed with the same key, because an STK push has to send
-- Safaricom a callback URL it can no longer recompute: without it, pushing
-- would be impossible or would mean showing the token on every screen forever.
-- A reader who holds the PII key can therefore mint a webhook URL -- and that
-- same reader already holds the consumer secrets in these rows. The raw token is
-- only ever shown in the create and rotate responses.
-- Shortcodes stay unique across organizations on purpose -- a callback
-- arrives with a shortcode and nothing else, so two organizations sharing one
-- code could not be told apart. In Daraja sandbox every test account is handed
-- the same paybill, so only one organization can hold a sandbox shortcode at a
-- time.
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
  callback_token_enc       VARBINARY(512)  NULL,
  is_active                TINYINT(1)      NOT NULL DEFAULT 1,
  created_at               DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at               DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_mpesa_shortcodes_env_code (environment, shortcode),
  UNIQUE KEY uq_mpesa_shortcodes_org_id (organization_id, id),
  KEY idx_mpesa_shortcodes_org (organization_id),
  KEY idx_mpesa_shortcodes_token (callback_token_hash),
  CONSTRAINT fk_mpesa_shortcodes_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT chk_mpesa_shortcodes_type CHECK (account_type IN ('paybill','till')),
  CONSTRAINT chk_mpesa_shortcodes_env CHECK (environment IN ('sandbox','production'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS mpesa_account_refs (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id   BIGINT UNSIGNED NOT NULL,
  shortcode_id      BIGINT UNSIGNED NOT NULL,
  lease_id          BIGINT UNSIGNED NOT NULL,
  account_reference VARCHAR(100)    NOT NULL COMMENT 'Value the tenant enters, e.g. KD-001-A1',
  is_active         TINYINT(1)      NOT NULL DEFAULT 1,
  created_at        DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at        DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_mpesa_refs_shortcode_ref (shortcode_id, account_reference),
  KEY idx_mpesa_refs_org_shortcode (organization_id, shortcode_id),
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

CREATE TABLE IF NOT EXISTS mpesa_transactions (
  id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id     BIGINT UNSIGNED NOT NULL,
  trans_id            VARCHAR(30) NOT NULL COMMENT 'M-Pesa receipt, e.g. SJK7XXXXXX',
  trans_type          VARCHAR(30) NULL,
  business_short_code VARCHAR(20) NOT NULL,
  shortcode_id        BIGINT UNSIGNED NULL,
  bill_ref_number     VARCHAR(100) NULL COMMENT 'Account number the tenant typed (unit / lease ref)',
  lease_id            BIGINT UNSIGNED NULL,
  msisdn              VARCHAR(64) NULL,
  first_name          VARCHAR(100) NULL,
  amount              DECIMAL(12,2) NOT NULL,
  trans_time          DATETIME NOT NULL,
  status              VARCHAR(30) NOT NULL DEFAULT 'unmatched',
  payment_id          BIGINT UNSIGNED NULL,
  raw_payload         JSON NULL,
  created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_mpesa_trans_id (trans_id),
  KEY idx_mpesa_org (organization_id),
  KEY idx_mpesa_shortcode (organization_id, shortcode_id),
  KEY idx_mpesa_org_lease (organization_id, lease_id),
  UNIQUE KEY uq_mpesa_trans_payment (organization_id, payment_id)
    COMMENT 'one C2B confirmation can back only one payment; a second receipt cannot reuse it',
  KEY idx_mpesa_bill_ref (bill_ref_number),
  KEY idx_mpesa_status (status),
  CONSTRAINT fk_mpesa_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_mpesa_shortcode
    FOREIGN KEY (organization_id, shortcode_id) REFERENCES mpesa_shortcodes (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_mpesa_lease
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_mpesa_payment_org
    FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT chk_mpesa_status CHECK (status IN ('unmatched','matched','refunded','ignored'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS mpesa_stk_requests (
  id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  organization_id      BIGINT UNSIGNED NOT NULL,
  shortcode_id         BIGINT UNSIGNED NULL,
  lease_id             BIGINT UNSIGNED NULL,
  invoice_id           BIGINT UNSIGNED NULL,
  phone                VARCHAR(20) NOT NULL,
  amount               DECIMAL(12,2) NOT NULL,
  merchant_request_id  VARCHAR(100) NULL,
  checkout_request_id  VARCHAR(100) NOT NULL,
  status               VARCHAR(30) NOT NULL DEFAULT 'pending',
  result_code          INT NULL,
  result_desc          VARCHAR(255) NULL,
  mpesa_receipt        VARCHAR(30) NULL,
  payment_id           BIGINT UNSIGNED NULL,
  created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_stk_checkout (checkout_request_id),
  KEY idx_stk_org (organization_id),
  KEY idx_stk_shortcode (organization_id, shortcode_id),
  KEY idx_stk_org_lease (organization_id, lease_id),
  KEY idx_stk_org_invoice (organization_id, invoice_id),
  UNIQUE KEY uq_stk_payment (organization_id, payment_id)
    COMMENT 'one STK request can back only one payment; two prompts cannot reuse it',
  KEY idx_stk_lease (lease_id),
  KEY idx_stk_invoice (invoice_id),
  CONSTRAINT fk_stk_org
    FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE RESTRICT,
  CONSTRAINT fk_stk_shortcode
    FOREIGN KEY (organization_id, shortcode_id) REFERENCES mpesa_shortcodes (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_stk_lease_org
    FOREIGN KEY (organization_id, lease_id) REFERENCES leases (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_stk_invoice_org
    FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT fk_stk_payment_org
    FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id)
    ON DELETE RESTRICT,
  CONSTRAINT chk_stk_status CHECK (status IN ('pending','success','failed','cancelled','timeout'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
