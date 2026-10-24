DROP TRIGGER IF EXISTS trg_pii_access_no_delete;
DROP TRIGGER IF EXISTS trg_pii_access_no_update;
DROP TABLE IF EXISTS pii_access_logs;
DROP TABLE IF EXISTS pii_retention_policies;

ALTER TABLE contractors
  DROP COLUMN pii_anonymized_at,
  DROP COLUMN pii_retention_until,
  DROP COLUMN kra_pin_enc,
  ADD COLUMN kra_pin VARCHAR(20) NULL AFTER specialty;

ALTER TABLE owners
  DROP COLUMN pii_anonymized_at,
  DROP COLUMN pii_retention_until,
  DROP COLUMN payout_account_name_enc,
  DROP COLUMN payout_bank_name_enc,
  DROP COLUMN payout_mpesa_number_enc,
  DROP COLUMN kra_pin_enc,
  ADD COLUMN kra_pin VARCHAR(20) NULL AFTER company,
  ADD COLUMN payout_mpesa_number VARCHAR(20) NULL AFTER payout_method,
  ADD COLUMN payout_bank_name VARCHAR(100) NULL,
  ADD COLUMN payout_account_name VARCHAR(150) NULL;

ALTER TABLE tenants
  DROP COLUMN pii_anonymized_at,
  DROP COLUMN pii_retention_until,
  DROP COLUMN income_enc,
  DROP COLUMN kra_pin_enc,
  ADD COLUMN kra_pin VARCHAR(20) NULL AFTER user_id,
  ADD COLUMN income DECIMAL(12,2) NULL AFTER employer_name;

ALTER TABLE users
  DROP KEY idx_users_national_id_hash,
  DROP COLUMN pii_anonymized_at,
  DROP COLUMN pii_retention_until,
  DROP COLUMN national_id_last4,
  DROP COLUMN national_id_hash,
  DROP COLUMN national_id_enc,
  ADD COLUMN national_id VARCHAR(30) NULL AFTER last_name,
  ADD KEY idx_users_national_id (national_id);
